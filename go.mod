module github.com/GitTsubasa/gumby

go 1.15

require (
	github.com/blevesearch/bleve/v2 v2.1.0
	github.com/blevesearch/bleve_index_api v1.0.1
	github.com/bwmarrin/discordgo v0.28.1
	github.com/kelseyhightower/envconfig v1.4.0
	github.com/liuzl/gocc v0.0.0-20231231122217-0372e1059ca5 // indirect
	github.com/stevenyao/go-opencc v0.0.0-20161014062826-cc376a51b65e
	golang.org/x/tools/gopls v0.15.2 // indirect
)

retract v0.1.0

replace github.com/bwmarrin/discordgo => github.com/FedorLap2006/discordgo v0.0.0-20210731173030-072a9ff725f9
