package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/kelseyhightower/envconfig"
)

type config struct {
	DiscordToken string
	DatabaseURL  string
}

type bot struct {
	db      *pgxpool.Pool
	discord *discordgo.Session
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

type definition struct {
	sourceName string
	readings   []string
	meanings   []string
}

func (b *bot) findEntries(ctx context.Context, words []string) (map[string][]*definition, error) {
	entries := make(map[string][]*definition)

	definitionsByID := make(map[int64]*definition)

	// Get all readings first.
	if err := func() error {
		rows, err := b.db.Query(ctx, `
			select
				id, word, readings, sources.name
			from
				definitions
			inner join
				sources on sources.code = definitions.source_code
			where
				word = any($1)
			order by id
		`, words)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var id int64
			var word string
			var readings []string
			var sourceName string

			if err := rows.Scan(&id, &word, &readings, &sourceName); err != nil {
				return err
			}

			definition := &definition{
				readings:   readings,
				sourceName: sourceName,
			}
			entries[word] = append(entries[word], definition)

			definitionsByID[id] = definition
		}

		if err := rows.Err(); err != nil {
			return err
		}

		return nil
	}(); err != nil {
		return nil, err
	}

	definitionIDs := make([]int64, 0, len(definitionsByID))
	for id := range definitionsByID {
		definitionIDs = append(definitionIDs, id)
	}

	// Get all meanings now.
	if err := func() error {
		rows, err := b.db.Query(ctx, `
			select
				definition_id, meaning
			from
				meanings
			where
				definition_id = any($1)
			order by definition_id, id
		`, definitionIDs)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var definitionID int64
			var meaning string

			if err := rows.Scan(&definitionID, &meaning); err != nil {
				return err
			}

			definitionsByID[definitionID].meanings = append(definitionsByID[definitionID].meanings, meaning)
		}

		if err := rows.Err(); err != nil {
			return err
		}

		return nil
	}(); err != nil {
		return nil, err
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

		count, err := b.count(ctx, payload.Query, payload.Sources)
		if err != nil {
			log.Printf("Failed to find words: %s", err)
			return
		}

		words, err := b.lookup(ctx, payload.Query, payload.Sources, queryLimit+1, payload.Page*queryLimit)
		if err != nil {
			log.Printf("Failed to find words: %s", err)
			return
		}

		hasNext := false
		if len(words) > queryLimit {
			words = words[:queryLimit]
			hasNext = true
		}

		entries, err := b.findEntries(ctx, words)
		if err != nil {
			log.Printf("Failed to find entries: %s", err)
			return
		}

		searchOutput, err := makeSearchOutput(payload.Query, payload.Sources, count, words, entries, payload.Page, hasNext)
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

		definitions, ok := entries[word]
		if !ok {
			log.Printf("Failed to get definitions")
			return
		}

		if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
			log.Printf("Failed to respond: %s", err)
			return
		}

		if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
			Embeds: []*discordgo.MessageEmbed{makeEntryOutput(word, definitions)},
		}); err != nil {
			log.Printf("Failed to edit response: %s", err)
			return
		}
	}
}

func (b *bot) count(ctx context.Context, query string, sources []string) (int, error) {
	var count int

	if err := b.db.QueryRow(ctx, `
		select
			count(*)
		from
			word_fts_tsvectors
		where
			tsvector @@ plainto_tsquery('english_nostop', $1) and
			(coalesce($2::text[], array[]::text[]) = array[]::text[] or source_code = any($2))
	`, query, sources).Scan(&count); err != nil {
		return 0, err
	}

	return count, nil
}

func (b *bot) lookup(ctx context.Context, query string, sources []string, limit int, offset int) ([]string, error) {
	var words []string

	rows, err := b.db.Query(ctx, `
		select
			word
		from
			word_fts_tsvectors
		where
			tsvector @@ plainto_tsquery('english_nostop', $1) and
			(coalesce($2::text[], array[]::text[]) = array[]::text[] or source_code = any($2))
		group by
			word
		order by
			max(ts_rank_cd(tsvector, plainto_tsquery('english_nostop', $1), 8)) desc
		limit $3 offset $4
	`, query, sources, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var word string
		if err := rows.Scan(&word); err != nil {
			return nil, err
		}

		words = append(words, word)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return words, nil
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

func makeSearchOutput(query string, sources []string, count int, words []string, entries map[string][]*definition, page int, hasNext bool) (*discordgo.WebhookEdit, error) {
	var selectMenuOptions []discordgo.SelectMenuOption
	for _, word := range words {
		var readings []string
		for _, definition := range entries[word] {
			readings = append(readings, definition.readings...)
		}

		var meanings []string
		for _, definition := range entries[word] {
			meanings = append(meanings, definition.meanings...)
		}

		selectMenuOptions = append(selectMenuOptions, discordgo.SelectMenuOption{
			Label:       fmt.Sprintf("%s (%s)", word, strings.Join(readings, ", ")),
			Description: truncate(strings.Join(meanings, ", "), 100, "..."),
			Value:       word,
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
						Placeholder: fmt.Sprintf("Select from results %d to %d", page*queryLimit+1, page*queryLimit+len(words)),
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

func makeEntryOutput(word string, definitions []*definition) *discordgo.MessageEmbed {
	prettyDefs := make([]string, len(definitions))
	for i, def := range definitions {
		prettyDefs[i] = fmt.Sprintf("**%s**\n%s\n_%s_", strings.Join(def.readings, ", "), strings.Join(def.meanings, ", "), def.sourceName)
	}

	return &discordgo.MessageEmbed{
		Title:       word,
		Color:       0x005BAC,
		Description: strings.Join(prettyDefs, "\n\n"),
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

	count, err := b.count(ctx, query, sources)
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
		log.Printf("Failed to count results: %s", err)
		return
	}

	if count == 0 {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("**0 results for “%s”**", query),
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0x4B5563,
						Description: "No results found.",
					},
				},
			},
		})
		return
	}

	words, err := b.lookup(ctx, query, sources, queryLimit+1, 0)
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
	if len(words) > queryLimit {
		words = words[:queryLimit]
		hasNext = true
	}

	entries, err := b.findEntries(ctx, words)
	if err != nil {
		log.Printf("Failed to find meanings: %s", err)
		return
	}

	searchOutput, err := makeSearchOutput(query, sources, count, words, entries, 0, hasNext)
	if err != nil {
		log.Printf("Failed to make search output: %s", err)
		return
	}

	var embeds []*discordgo.MessageEmbed
	if len(words) == 1 {
		word := words[0]

		entries, err := b.findEntries(ctx, []string{word})
		if err != nil {
			log.Printf("Failed to get entries: %s", err)
			return
		}

		definitions, ok := entries[word]
		if !ok {
			log.Printf("Failed to get definitions", err)
			return
		}

		embeds = []*discordgo.MessageEmbed{makeEntryOutput(word, definitions)}
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

func (b *bot) sources(ctx context.Context) ([]source, error) {
	rows, err := b.db.Query(ctx, `
		select code, name from sources order by code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []source
	for rows.Next() {
		var s source
		if err := rows.Scan(&s.code, &s.name); err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}

	return sources, nil
}

func main() {
	var c config
	if err := envconfig.Process("gumby", &c); err != nil {
		log.Fatalf("Failed to parse envconfing: %s", err)
	}

	db, err := pgxpool.Connect(context.Background(), c.DatabaseURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
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

	bot := bot{db: db, discord: discord}

	ss, err := bot.sources(context.Background())
	if err != nil {
		log.Fatalf("Unable to list sources: %v\n", err)
	}

	choices := make([]*discordgo.ApplicationCommandOptionChoice, len(ss))
	for i, s := range ss {
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
