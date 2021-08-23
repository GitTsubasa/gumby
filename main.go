package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sort"

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
		case "crawford":
			b.handleCrawford(ctx, i)
		}

	case discordgo.InteractionMessageComponent:
		b.handleComponentInteraction(ctx, i)
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

	sources := make([]*discordgo.ApplicationCommandOptionChoice, len(sortedSources))
	for i, s := range sortedSources {
		sources[i] = &discordgo.ApplicationCommandOptionChoice{Name: s.name, Value: s.code}
	}

	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "shdef",
			Description: "Look up in dictionary.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: "What to look up (by word, meaning, or reading)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "dict",
					Description: "Which dictionary to look up from",
					Required:    false,
					Choices:     sources,
				},
			},
		},

		{
			Name:        "crawford",
			Description: "Look up a character in Crawford (kaudip'e) script.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reading",
					Description: "What reading to look up",
					Required:    true,
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
