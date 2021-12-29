package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/bwmarrin/discordgo"
	"github.com/kelseyhightower/envconfig"

	_ "github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/en"
	_ "github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	_ "github.com/blevesearch/bleve/v2/analysis/tokenizer/single"
	_ "github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	_ "github.com/blevesearch/bleve/v2/analysis/tokenizer/whitespace"
)

type config struct {
	DiscordToken string
	IndexPath    string
}

type bot struct {
	index   bleve.Index
	discord *discordgo.Session
}

type actionGoToPage struct {
	Query  string `json:"query"`
	Source string `json:"source"`
	Page   int    `json:"page"`
}

var defaultFooter = &discordgo.MessageEmbedFooter{
	Text: "www.shanghaivernacular.com",
}

func (b *bot) handleInteraction(ctx context.Context, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		name := i.ApplicationCommandData().Name
		switch name {
		case "gumby":
			b.handleHelp(ctx, i)
		case "def":
			b.handleShdef(ctx, i, "")
		case "homophone":
			b.handleHomophone(ctx, i, "qianplus")
		default:
			b.handleShdef(ctx, i, name)
		}

	case discordgo.InteractionMessageComponent:
		b.handleComponentInteraction(ctx, i)
	}
}

func (b *bot) handleHelp(ctx context.Context, i *discordgo.InteractionCreate) {
	var fields []*discordgo.MessageEmbedField
	for k, v := range sources {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:  "`/" + k + "`",
			Value: v,
		})
	}

	sort.Slice(fields, func(i int, j int) bool {
		return fields[i].Name < fields[j].Name
	})

	b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{
				{
					Color:       0x005BAC,
					Title:       "Hi! I'm Gumby!",
					Description: "I'm a bot that looks up words in Shanghainese dictionaries! Here's a list of dictionaries you can use below, or you can use **`/def`** to search all dictionaries!",
					Fields:      fields,
					Footer:      defaultFooter,
				},
			},
		},
	})
}

func (b *bot) handleComponentInteraction(ctx context.Context, i *discordgo.InteractionCreate) {
	customID := i.Interaction.MessageComponentData().CustomID

	pipeIndex := strings.IndexRune(customID, '|')
	prefix := customID[:pipeIndex]
	rawPayload := []byte(customID[pipeIndex+1:])

	switch prefix {
	case customIDPrefixShdefGoToPage:
		var payload actionGoToPage
		if err := json.Unmarshal(rawPayload, &payload); err != nil {
			log.Printf("Failed to unmarshal payload words: %s", err)
			return
		}
		b.handleShdefGoToPage(ctx, i.Interaction, payload.Query, payload.Source, payload.Page)

	case customIDPrefixShdefSelect:
		word := i.Interaction.MessageComponentData().Values[0]
		b.handleShdefSelect(ctx, i.Interaction, word)
	}
}

var sources = map[string]string{
	"char":     "Chinese characters used between 1870–1910",
	"repub":    "Formal Republican terms",
	"qianplus": "Qian Nairong's dictionary 2nd ed. + extras",
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

	discord.UpdateGameStatus(0, "/gumby for help!")

	log.Printf("Connected to Discord.")

	defer discord.Close()

	bot := bot{index: index, discord: discord}

	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "gumby",
			Description: "Let me tell you who I am and what I do!",
		},
		{
			Name:        "def",
			Description: "Look up in all dictionaries",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: "What to look up (by word, meaning, or reading)",
					Required:    true,
				},
			},
		},
		{
			Name:        "homophone",
			Description: "Find homophones",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: "Characters to look up",
					Required:    true,
				},
			},
		},
	}
	for k, v := range sources {
		commands = append(commands, &discordgo.ApplicationCommand{
			Name:        k,
			Description: "Look up in dictionary: " + v,
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: "What to look up (by word, meaning, or reading)",
					Required:    true,
				},
			},
		})
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

func (b *bot) handleWordSelect(ctx context.Context, interaction *discordgo.Interaction, word string) {
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

	if err := b.discord.InteractionRespond(interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
		log.Printf("Failed to respond: %s", err)
		return
	}

	if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, interaction, &discordgo.WebhookEdit{
		Embeds: []*discordgo.MessageEmbed{makeEntryOutput(entry)},
	}); err != nil {
		log.Printf("Failed to edit response: %s", err)
		return
	}
}

func (b *bot) handleGoToPage(ctx context.Context, interaction *discordgo.Interaction, results []result, query string, source string, count uint64, page int, hasNext bool, customIDPrefix string) {
	resultIDs := make([]string, len(results))
	for i, r := range results {
		resultIDs[i] = r.id
	}

	entries, err := b.findEntries(ctx, resultIDs)
	if err != nil {
		log.Printf("Failed to find entries: %s", err)
		return
	}

	searchOutput, err := makeSearchOutput(query, source, count, resultIDs, entries, page, hasNext, customIDPrefix)
	if err != nil {
		log.Printf("Failed to make search output: %s", err)
		return
	}

	if err := b.discord.InteractionRespond(interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
		log.Printf("Failed to respond: %s", err)
		return
	}

	if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, interaction, searchOutput); err != nil {
		log.Printf("Failed to edit response: %s", err)
		return
	}
}

func (b *bot) handleGoToInitialPage(ctx context.Context, interaction *discordgo.Interaction, results []result, query string, source string, count uint64, hasNext bool, customIDPrefix string) {
	resultIDs := make([]string, len(results))
	for i, r := range results {
		resultIDs[i] = r.id
	}

	entries, err := b.findEntries(ctx, resultIDs)
	if err != nil {
		log.Printf("Failed to find meanings: %s", err)
		return
	}

	searchOutput, err := makeSearchOutput(query, source, count, resultIDs, entries, 0, hasNext, customIDPrefix)
	if err != nil {
		log.Printf("Failed to make search output: %s", err)
		return
	}

	var embeds []*discordgo.MessageEmbed
	if len(results) == 1 || (len(results) > 0 && isExactMatch(results[0], query) && !isExactMatch(results[1], query)) {
		entries, err := b.findEntries(ctx, resultIDs)
		if err != nil {
			log.Printf("Failed to get entries: %s", err)
			return
		}

		entry, ok := entries[resultIDs[0]]
		if !ok {
			log.Printf("Failed to get definitions")
			return
		}

		embeds = []*discordgo.MessageEmbed{makeEntryOutput(entry)}
	}

	if err := b.discord.InteractionRespond(interaction, &discordgo.InteractionResponse{
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
