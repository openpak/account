# Deploy: account

## Run

```sh
cp example.env .env   # fill in required values
set -a; source .env; set +a
go run ./cmd/account
```

Requires PostgreSQL. Migrations apply automatically at startup.

## Tests


```sh
docker run -d --name openpak-pg-test -e POSTGRES_PASSWORD=test \
  -e POSTGRES_DB=account_test -p 54329:5432 postgres:18-alpine
go vet -tags integration ./...
go test -tags integration -race -count=1 ./...
```

The tests drop and recreate the `public` schema of that database. To use a
different one, set `ACCOUNT_TEST_DATABASE_URL`, for example
`postgres://postgres:x@127.0.0.1:25432/account_test?sslmode=disable`.
