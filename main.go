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
	index   bleve.Index
	discord *discordgo.Session
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
		log.Println("hello from addhandler")
	})

	stop := make(chan os.Signal)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
