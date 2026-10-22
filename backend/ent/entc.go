//go:build ignore

package main

import (
	"log"
	"os"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
	"github.com/63747756/jingjiaagent/backend/pkg/entx"
)

func main() {
	target := os.Getenv("JINGJIAAGENT_ENT_TARGET")
	if target == "" {
		target = "../db"
	}
	if err := entc.Generate(
		"./schema",
		&gen.Config{
			Target:  target,
			Package: "github.com/63747756/jingjiaagent/backend/db",
			Features: []gen.Feature{
				gen.FeatureUpsert,
				gen.FeatureModifier,
				gen.FeatureExecQuery,
				gen.FeatureIntercept,
				gen.FeatureLock,
			},
		},
		entc.Extensions(
			&entx.Cursor{},
			&entx.Page{},
		),
	); err != nil {
		log.Fatal("running ent codegen:", err)
	}
}
