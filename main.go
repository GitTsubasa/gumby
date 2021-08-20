package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"

	"github.com/blevesearch/bleve/v2"
	index "github.com/blevesearch/bleve_index_api"
	"github.com/bwmarrin/discordgo"
	"github.com/kelseyhightower/envconfig"
)

type config struct {
	DiscordToken string
	IndexPath    string
}

type bot struct {
	index       bleve.Index
	discord     *discordgo.Session
	sourceNames map[string]string
}

func (b *bot) handleInteraction(ctx context.Context, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		switch i.ApplicationCommandData().Name {
		case "shdef":
			b.handleShdef(ctx, i)
		}
	case discordgo.InteractionMessageComponent:
		b.handleComponentInteraction(ctx, i)
	}
}

type shdefActionGoToPage struct {
	Query   string   `json:"query"`
	Sources []string `json:"sources"`
	Page    int      `json:"page"`
}

const (
	customIDPrefixShdefGoToPage string = "shdef:goToPage"
	customIDPrefixShdefSelect   string = "shdef:select"
)

type entry struct {
	word        string
	sourceName  string
	definitions []definition
}

type definition struct {
	readings []string
	meanings []string
}

func (b *bot) findEntries(ctx context.Context, ids []string) (map[string]entry, error) {
	entries := make(map[string]entry)

	for _, id := range ids {
		doc, err := b.index.Document(id)
		if err != nil {
			return nil, err
		}

		var e entry
		doc.VisitFields(func(f index.Field) {
			arrayPositions := f.ArrayPositions()

			switch f.Name() {
			case "word":
				e.word = string(f.Value())
			case "definitions.meanings":
				for len(e.definitions) <= int(arrayPositions[0]) {
					e.definitions = append(e.definitions, definition{})
				}

				e.definitions[int(arrayPositions[0])].meanings = append(e.definitions[int(arrayPositions[0])].meanings, string(f.Value()))
			case "definitions.readings":
				for len(e.definitions) <= int(arrayPositions[0]) {
					e.definitions = append(e.definitions, definition{})
				}

				e.definitions[int(arrayPositions[0])].readings = append(e.definitions[int(arrayPositions[0])].readings, string(f.Value()))
			case "source_code":
				e.sourceName = b.sourceNames[string(f.Value())]
			}
		})

		entries[id] = e
	}

	return entries, nil
}

func (b *bot) handleComponentInteraction(ctx context.Context, i *discordgo.InteractionCreate) {
	customID := i.Interaction.MessageComponentData().CustomID

	pipeIndex := strings.IndexRune(customID, '|')
	prefix := customID[:pipeIndex]
	rawPayload := []byte(customID[pipeIndex+1:])

	switch prefix {
	case customIDPrefixShdefGoToPage:
		var payload shdefActionGoToPage
		if err := json.Unmarshal(rawPayload, &payload); err != nil {
			log.Printf("Failed to unmarshal payload words: %s", err)
			return
		}

		results, count, err := b.lookup(ctx, payload.Query, payload.Sources, queryLimit+1, payload.Page*queryLimit)
		if err != nil {
			log.Printf("Failed to find words: %s", err)
			return
		}

		hasNext := false
		if len(results) > queryLimit {
			results = results[:queryLimit]
			hasNext = true
		}

		entries, err := b.findEntries(ctx, results)
		if err != nil {
			log.Printf("Failed to find entries: %s", err)
			return
		}

		searchOutput, err := makeSearchOutput(payload.Query, payload.Sources, count, results, entries, payload.Page, hasNext)
		if err != nil {
			log.Printf("Failed to make search output: %s", err)
			return
		}

		if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
			log.Printf("Failed to respond: %s", err)
			return
		}

		if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, i.Interaction, searchOutput); err != nil {
			log.Printf("Failed to edit response: %s", err)
			return
		}

	case customIDPrefixShdefSelect:
		word := i.Interaction.MessageComponentData().Values[0]

		entries, err := b.findEntries(ctx, []string{word})
		if err != nil {
			log.Printf("Failed to get entries: %s", err)
			return
		}

		entry, ok := entries[word]
		if !ok {
			log.Printf("Failed to get entry")
			return
		}

		if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
			log.Printf("Failed to respond: %s", err)
			return
		}

		if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
			Embeds: []*discordgo.MessageEmbed{makeEntryOutput(entry)},
		}); err != nil {
			log.Printf("Failed to edit response: %s", err)
			return
		}
	}
}

func (b *bot) lookup(ctx context.Context, query string, sources []string, limit int, offset int) ([]string, uint64, error) {
	req := bleve.NewSearchRequest(bleve.NewMatchQuery(query))
	req.Size = limit
	req.From = offset

	r, err := b.index.Search(req)
	if err != nil {
		return nil, 0, err
	}

	ids := make([]string, len(r.Hits))
	for i, hit := range r.Hits {
		ids[i] = hit.ID
	}

	return ids, r.Total, nil
}

func truncate(s string, length int, ellipsis string) string {
	if len(s) <= length {
		return s
	}

	length -= len(ellipsis)
	var buf strings.Builder
	for _, r := range []rune(s) {
		next := string(r)
		if buf.Len()+len(next) > length {
			break
		}
		buf.WriteString(next)
	}

	return buf.String() + ellipsis
}

func makeSearchOutput(query string, sources []string, count uint64, ids []string, entries map[string]entry, page int, hasNext bool) (*discordgo.WebhookEdit, error) {
	var selectMenuOptions []discordgo.SelectMenuOption
	for _, id := range ids {
		entry := entries[id]

		var readings []string
		for _, definition := range entry.definitions {
			readings = append(readings, definition.readings...)
		}

		var meanings []string
		for _, definition := range entry.definitions {
			meanings = append(meanings, definition.meanings...)
		}

		selectMenuOptions = append(selectMenuOptions, discordgo.SelectMenuOption{
			Label:       fmt.Sprintf("%s (%s)", entry.word, strings.Join(readings, ", ")),
			Description: truncate(strings.Join(meanings, ", "), 100, "..."),
			Value:       id,
		})
	}

	var title string
	var components []discordgo.MessageComponent
	if count == 1 {
		title = fmt.Sprintf("1 result for “%s”", query)
	} else {
		title = fmt.Sprintf("%d results for “%s”", count, query)

		prevPagePayload, err := json.Marshal(shdefActionGoToPage{Query: query, Sources: sources, Page: page - 1})
		if err != nil {
			return nil, err
		}

		nextPagePayload, err := json.Marshal(shdefActionGoToPage{Query: query, Sources: sources, Page: page + 1})
		if err != nil {
			return nil, err
		}

		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.SelectMenu{
						Placeholder: fmt.Sprintf("Select from results %d to %d", page*queryLimit+1, page*queryLimit+len(entries)),
						Options:     selectMenuOptions,
						CustomID:    customIDPrefixShdefSelect + "|",
					},
				},
			},
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Emoji:    discordgo.ComponentEmoji{Name: "◀️"},
						Label:    "Previous Page",
						Style:    discordgo.SecondaryButton,
						Disabled: page == 0,
						CustomID: customIDPrefixShdefGoToPage + "|" + string(prevPagePayload),
					},
					discordgo.Button{
						Emoji:    discordgo.ComponentEmoji{Name: "▶️"},
						Label:    "Next Page",
						Style:    discordgo.SecondaryButton,
						Disabled: !hasNext,
						CustomID: customIDPrefixShdefGoToPage + "|" + string(nextPagePayload),
					},
				},
			},
		}
	}

	return &discordgo.WebhookEdit{
		Content:    fmt.Sprintf("**%s**", title),
		Components: components,
	}, nil
}

func makeEntryOutput(e entry) *discordgo.MessageEmbed {
	prettyDefs := make([]string, len(e.definitions))
	for i, def := range e.definitions {
		prettyDefs[i] = fmt.Sprintf("**%s**\n%s", strings.Join(def.readings, ", "), strings.Join(def.meanings, ", "))
	}

	return &discordgo.MessageEmbed{
		Title:       e.word,
		Color:       0x005BAC,
		Description: fmt.Sprintf("%s\n\n_%s_", strings.Join(prettyDefs, "\n\n"), e.sourceName),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "www.shanghaivernacular.com",
		},
	}
}

const queryLimit = 25

func (b *bot) handleShdef(ctx context.Context, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options

	query := strings.TrimSpace(options[0].StringValue())

	var sources []string
	if len(options) > 1 {
		sourceCode := options[1].StringValue()
		sources = []string{sourceCode}
	}

	if query == "" {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0xDC2626,
						Description: "You have to provide something to look up!",
					},
				},
			},
		})
		return
	}

	results, count, err := b.lookup(ctx, query, sources, queryLimit+1, 0)
	if err != nil {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0xDC2626,
						Description: "An error occurred.",
					},
				},
			},
		})
		log.Printf("Failed to lookup word: %s", err)
		return
	}

	hasNext := false
	if len(results) > queryLimit {
		results = results[:queryLimit]
		hasNext = true
	}

	entries, err := b.findEntries(ctx, results)
	if err != nil {
		log.Printf("Failed to find meanings: %s", err)
		return
	}

	searchOutput, err := makeSearchOutput(query, sources, count, results, entries, 0, hasNext)
	if err != nil {
		log.Printf("Failed to make search output: %s", err)
		return
	}

	var embeds []*discordgo.MessageEmbed
	if len(results) == 1 {
		entries, err := b.findEntries(ctx, results)
		if err != nil {
			log.Printf("Failed to get entries: %s", err)
			return
		}

		entry, ok := entries[results[0]]
		if !ok {
			log.Printf("Failed to get definitions")
			return
		}

		embeds = []*discordgo.MessageEmbed{makeEntryOutput(entry)}
	}

	if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     embeds,
			Content:    searchOutput.Content,
			Components: searchOutput.Components,
		},
	}); err != nil {
		log.Printf("Failed to send interaction: %s", err)
		return
	}
}

type source struct {
	code string
	name string
}

func main() {
	var c config
	if err := envconfig.Process("gumby", &c); err != nil {
		log.Fatalf("Failed to parse envconfing: %s", err)
	}

	index, err := bleve.Open(c.IndexPath)
	if err != nil {
		log.Fatalf("Unable to open index: %v\n", err)
	}

	log.Printf("Connected to database.")

	discord, err := discordgo.New(c.DiscordToken)
	if err != nil {
		log.Fatalf("Unable to connect to Discord: %v\n", err)
	}

	discord.StateEnabled = false
	discord.Identify.Intents = discordgo.IntentsGuilds

	if err := discord.Open(); err != nil {
		log.Fatalf("Unable to connect to Discord: %v\n", err)
	}

	discord.UpdateGameStatus(0, "/shdef")

	log.Printf("Connected to Discord.")

	defer discord.Close()

	bot := bot{index: index, discord: discord, sourceNames: map[string]string{
		"c": "Chinese characters used between 1870–1910",
		"r": "Formal Republican terms",
	}}

	type source struct {
		code string
		name string
	}
	sortedSources := make([]source, 0, len(bot.sourceNames))

	for code, name := range bot.sourceNames {
		sortedSources = append(sortedSources, source{code: code, name: name})
	}

	sort.Slice(sortedSources, func(i int, j int) bool {
		return sortedSources[i].code < sortedSources[j].code
	})

	choices := make([]*discordgo.ApplicationCommandOptionChoice, len(sortedSources))
	for i, s := range sortedSources {
		choices[i] = &discordgo.ApplicationCommandOptionChoice{Name: s.name, Value: s.code}
	}

	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "shdef",
			Description: "Look up in dictionary.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: "What to look up (by word or by meaning)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "dict",
					Description: "Which dictionary to look up from",
					Required:    false,
					Choices:     choices,
				},
			},
		},
	}

	discord.AddHandler(func(d *discordgo.Session, g *discordgo.GuildCreate) {
		oldCmds, err := discord.ApplicationCommands(discord.State.User.ID, g.Guild.ID)
		if err != nil {
			log.Printf("Unable to get commands for %s: %v\n", g.Guild.ID, err)
			return
		}

		for _, cmd := range oldCmds {
			if err := discord.ApplicationCommandDelete(discord.State.User.ID, g.Guild.ID, cmd.ID); err != nil {
				log.Printf("Unable to delete command %s for %s: %v\n", cmd.Name, g.Guild.ID, err)
				continue
			}
			log.Printf("Deleted command %s for %s", cmd.Name, g.Guild.ID)
		}

		for _, cmd := range commands {
			if _, err := discord.ApplicationCommandCreate(discord.State.User.ID, g.Guild.ID, cmd); err != nil {
				log.Printf("Unable to create command %s for %s: %v\n", cmd.Name, g.Guild.ID, err)
				continue
			}
			log.Printf("Created command %s for %s", cmd.Name, g.Guild.ID)
		}
	})

	discord.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		bot.handleInteraction(context.Background(), i)
	})

	stop := make(chan os.Signal)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
