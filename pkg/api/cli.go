package api

import (
	dataaggregator "github.com/travigo/travigo/pkg/dataaggregator/global"
	"github.com/travigo/travigo/pkg/elastic_client"
	"github.com/travigo/travigo/pkg/redis_client"
	"github.com/urfave/cli/v2"
)

func RegisterCLI() *cli.Command {
	return &cli.Command{
		Name:  "web-api",
		Usage: "Provides the core web API",
		Subcommands: []*cli.Command{
			{
				Name:  "run",
				Usage: "run web api server",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "listen",
						Value: ":8080",
						Usage: "listen target for the web server",
					},
				},
				Action: func(c *cli.Context) error {
					if err := elastic_client.Connect(false); err != nil {
						return err
					}
					if err := redis_client.Connect(); err != nil {
						return err
					}

					dataaggregator.Setup()

					return SetupServer(c.String("listen"))
				},
			},
		},
	}
}
