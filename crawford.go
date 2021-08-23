package main

import (
	"context"

	"github.com/bwmarrin/discordgo"
)

func (b *bot) handleCrawford(ctx context.Context, i *discordgo.InteractionCreate) {
	b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{
				{
					Color:       0xDC2626,
					Description: "working on it!",
				},
			},
		},
	})
}
