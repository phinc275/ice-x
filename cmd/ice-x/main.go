package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/hiendaovinh/toolkit/pkg/db"
	"github.com/joho/godotenv"
	ice_x "github.com/phinc275/ice-x/internal/ice-x"
	"github.com/phinc275/ice-x/internal/ice-x/datastore"
	"github.com/phinc275/ice-x/internal/ice-x/handler"
	"github.com/urfave/cli/v2"
)

func init() {
	//nolint:errcheck
	godotenv.Load()
}

func main() {
	app := &cli.App{
		Name:  "Ice X",
		Usage: "Wrapped X APIs by Icetea Labs",
		Commands: []*cli.Command{
			newServeCommand(),
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func newServeCommand() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "start the web server",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "addr",
				Value: "0.0.0.0:8087",
				Usage: "serve address",
			},
		},
		Action: func(c *cli.Context) error {
			origins := make([]string, 0)
			if os.Getenv("ORIGINS") != "" {
				origins = strings.Split(os.Getenv("ORIGINS"), ",")
			}
			adminIDS := make([]string, 0)
			if os.Getenv("ADMIN_IDS") != "" {
				adminIDS = strings.Split(os.Getenv("ADMIN_IDS"), ",")
			}

			dbHost := os.Getenv("DB_HOST")
			dbPort := os.Getenv("DB_PORT")
			dbUser := os.Getenv("DB_USER")
			dbPassword := os.Getenv("DB_PASSWORD")
			dbName := os.Getenv("DB_DATABASE")
			pgxPool, err := db.InitPGXPool(&db.PostgresConfig{
				Host:     dbHost,
				Port:     dbPort,
				Database: dbName,
				User:     dbUser,
				Password: dbPassword,
			})
			if err != nil {
				return err
			}

			xAccountDs, err := datastore.NewXAccountDatastorePgx(pgxPool)
			if err != nil {
				return err
			}

			manager := ice_x.NewManager(xAccountDs)
			err = manager.LoadFromCookies(c.Context)
			if err != nil {
				return err
			}

			handlerCfg := &handler.Config{
				Mode:     os.Getenv("MODE"),
				Origins:  origins,
				AdminIDs: adminIDS,
				Jwks:     os.Getenv("ID_JWKS"),
			}

			h, err := handler.New(handlerCfg, manager)
			if err != nil {
				return err
			}

			srv := &http.Server{
				Addr:    c.String("addr"),
				Handler: h,
			}

			quit := make(chan os.Signal, 1)

			go func() {
				log.Printf("ListenAndServe: %s (%s)\n", c.String("addr"), os.Getenv("MODE"))
				err := srv.ListenAndServe()
				if err != nil {
					log.Printf("ListenAndServe failed: %s\n", err)
					quit <- os.Kill
				} else {
					quit <- os.Interrupt
				}
			}()

			signal.Notify(quit, os.Interrupt)
			<-quit

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			return srv.Shutdown(shutdownCtx)
		},
	}
}
