.PHONY: image swag generate check-generate migrate_sql

# Builds the owned backend and frontend images with independent lock revisions.
image:
	python ../runtime/jingjiaagent/build_web.py

swag:
	python scripts/generate-openapi.py

generate:
	go generate ./ent

check-generate:
	@echo "Checking if generated code is up to date..."
	@go generate ./ent
	@git diff --quiet -- db/ || (echo "Error: Generated code out of sync with schema. Run 'make generate' and commit the changes." && exit 1)
	@echo "Generated code is up to date."

migrate_sql:
	migrate create -ext sql -dir migration -seq ${SEQ}
