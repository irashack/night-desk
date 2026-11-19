# Contributing to Night Desk

Thanks for helping. Bug reports, ideas and pull requests are all welcome.

## Before you start

- **Bugs:** open an issue with the release or image tag you run, what you did, what you expected and what happened. Leave out keys, reader
  identities and private configuration.
- **Features and larger changes:** open an issue first so we can agree on the
  shape before you spend time on code. Night Desk keeps a few deliberate limits
  (one binary, SQLite, a reader that works without JavaScript, a worker that never
  opens the database); [docs/design.md](docs/design.md) explains why.
- **Security problems:** don't open a public issue; see [SECURITY.md](SECURITY.md).

## Development

You need Go (the version in `go.mod`). Node is only used for the service-worker
smoke tests, and Python only for the Hermes integration tests.

```sh
go vet ./...
gofmt -l .            # must print nothing
go test ./...
node --test web/sw_test.js
```

If you change the edition types or the API, regenerate the published contracts
and commit the results:

```sh
go generate ./internal/edition          # schema/edition-v*.json
go run ./internal/api/openapi-gen docs/reference/openapi.json
```

CI checks that both are up to date.

## Pull requests

- Keep each pull request to one change, with tests for the behaviour it adds or
  fixes.
- Explain *why* in the description; the diff already shows what.
- Changes to the edition schema, the HTTP API, configuration or database
  migrations need extra care: editions in schema v1 and v2 are accepted
  permanently, and migrations only move forward (see
  [docs/upgrading.md](docs/upgrading.md)).
- New third-party Go modules need a stated reason.

By contributing you agree that your contribution is licensed under the
[MIT licence](LICENSE).
