package main

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
	"github.com/urfave/cli/v2"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

func init() {
	//nolint:errcheck
	godotenv.Load()
}

func main() {
	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbUser := os.Getenv("DB_USER")
	dbPassword := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_DATABASE")

	db, err := sql.Open("pgx", fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", dbUser, dbPassword, dbHost, dbPort, dbName))
	if err != nil {
		log.Fatal(err)
	}

	goose.SetBaseFS(embedMigrations)

	app := &cli.App{
		Name:  "migrate",
		Usage: "database migration",
		Commands: []*cli.Command{
			commandAuto(),
			commandUp(),
			commandDown(),
			commandStatus(),
		},
		Metadata: map[string]any{
			"db": db,
		},
	}

	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatal(err)
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func commandAuto() *cli.Command {
	return &cli.Command{
		Name: "auto",
		Action: func(c *cli.Context) error {
			if os.Getenv("AUTO_MIGRATE") != "1" {
				return nil
			}

			db, ok := c.App.Metadata["db"].(*sql.DB)
			if !ok {
				return errors.New("invalid DB")
			}

			return goose.Up(db, "migrations")
		},
	}
}

func commandUp() *cli.Command {
	return &cli.Command{
		Name: "up",
		Action: func(c *cli.Context) error {
			db, ok := c.App.Metadata["db"].(*sql.DB)
			if !ok {
				return errors.New("invalid DB")
			}

			return goose.Up(db, "migrations")
		},
	}
}

func commandDown() *cli.Command {
	return &cli.Command{
		Name: "down",
		Action: func(c *cli.Context) error {
			db, ok := c.App.Metadata["db"].(*sql.DB)
			if !ok {
				return errors.New("invalid DB")
			}

			return goose.Down(db, "migrations")
		},
	}
}

func commandStatus() *cli.Command {
	return &cli.Command{
		Name: "status",
		Action: func(c *cli.Context) error {
			db, ok := c.App.Metadata["db"].(*sql.DB)
			if !ok {
				return errors.New("invalid DB")
			}

			return goose.Status(db, "migrations")
		},
	}
}
