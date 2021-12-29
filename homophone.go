package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/bwmarrin/discordgo"
)

type homophoneActionGoToPage struct {
	Query  string `json:"query"`
	Source string `json:"source"`
	Page   int    `json:"page"`
}

const (
	customIDPrefixHomophoneGoToPage string = "homophone:goToPage"
	customIDPrefixHomophoneSelect   string = "homophone:select"
)

func (b *bot) lookupHomophone(ctx context.Context, q string, source string, limit int, offset int) ([]result, uint64, error) {
	q = strings.TrimSpace(q)

	// Find all readings for characters in q.
	matches := make([]query.Query, len(q))
	for i, c := range q {
		wordMatch := bleve.NewMatchPhraseQuery(string(c))
		wordMatch.SetField("word")

		req := bleve.NewSearchRequest(wordMatch)
		req.Fields = []string{"definitions.readings"}

		r, err := b.index.Search(req)
		if err != nil {
			return nil, 0, err
		}

		if len(r.Hits) == 0 {
			// TODO: better feedback on this case.
			return nil, 0, nil
		}

		var readingMatches []query.Query
		for _, hit := range r.Hits {
			for _, reading := range fieldToStringList(hit.Fields["definitions.readings"]) {
				readingsMatch := bleve.NewMatchPhraseQuery(reading)
				readingsMatch.SetField("definitions.readings")
				readingMatches = append(readingMatches, readingsMatch)
			}
		}

		matches[i] = bleve.NewDisjunctionQuery(readingMatches...)
	}

	var sourceMatch query.Query = bleve.NewMatchAllQuery()
	if source != "" {
		realSourceMatch := bleve.NewMatchPhraseQuery(source)
		realSourceMatch.SetField("source")
		sourceMatch = realSourceMatch
	}

	req := bleve.NewSearchRequest(bleve.NewConjunctionQuery(bleve.NewConjunctionQuery(matches...), sourceMatch))
	req.Size = limit
	req.From = offset
	req.Fields = []string{"word", "simplified", "definitions.readings", "definitions.readings_no_diacritics", "source"}

	r, err := b.index.Search(req)
	if err != nil {
		return nil, 0, err
	}

	results := make([]result, len(r.Hits))
	for i, hit := range r.Hits {
		results[i] = result{
			id:                   hit.ID,
			word:                 hit.Fields["word"].(string),
			simplified:           fieldToStringList(hit.Fields["simplified"]),
			readings:             fieldToStringList(hit.Fields["definitions.readings"]),
			readingsNoDiacritics: fieldToStringList(hit.Fields["definitions.readings_no_diacritics"]),
			source:               hit.Fields["source"].(string),
		}
	}

	return results, r.Total, nil
}

func (b *bot) handleHomophone(ctx context.Context, i *discordgo.InteractionCreate, source string) {
	options := i.ApplicationCommandData().Options

	query := strings.TrimSpace(options[0].StringValue())

	if query == "" {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0xDC2626,
						Description: "You have to provide something to look up!",
						Footer:      defaultFooter,
					},
				},
			},
		})
		return
	}

	results, count, err := b.lookupHomophone(ctx, query, source, queryLimit+1, 0)
	if err != nil {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0xDC2626,
						Description: "An error occurred.",
						Footer:      defaultFooter,
					},
				},
			},
		})
		log.Printf("Failed to lookup word: %s", err)
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
						Footer:      defaultFooter,
					},
				},
			},
		})
		return
	}

	hasNext := false
	if len(results) > queryLimit {
		results = results[:queryLimit]
		hasNext = true
	}

	b.handleGoToInitialPage(ctx, i.Interaction, results, query, source, count, hasNext, customIDPrefixHomophoneGoToPage)
}

func (b *bot) handleHomophoneGoToPage(ctx context.Context, interaction *discordgo.Interaction, query string, source string, page int) {
	results, count, err := b.lookupHomophone(ctx, query, source, queryLimit+1, page*queryLimit)
	if err != nil {
		log.Printf("Failed to find words: %s", err)
		return
	}

	hasNext := false
	if len(results) > queryLimit {
		results = results[:queryLimit]
		hasNext = true
	}

	b.handleGoToPage(ctx, interaction, results, query, source, count, page, hasNext, customIDPrefixHomophoneGoToPage)
}

func (b *bot) handleHomophoneSelect(ctx context.Context, interaction *discordgo.Interaction, word string) {
	b.handleWordSelect(ctx, interaction, word)
}
