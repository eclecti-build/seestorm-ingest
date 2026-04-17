data "external_schema" "ent" {
  program = [
    "go",
    "run",
    "-mod=mod",
    "ariga.io/atlas-provider-ent/cmd/atlas-provider-ent",
    "--path", "./ent/schema",
    "--dialect", "postgres",
  ]
}

env "local" {
  src = data.external_schema.ent.url
  url = getenv("ATLAS_LOCAL_URL")
  dev = "docker://postgres/16/dev?search_path=public"
  migration {
    dir = "file://ent/migrate/migrations"
  }
}

env "prod" {
  src = data.external_schema.ent.url
  url = getenv("DATABASE_URL")
  dev = "docker://postgres/16/dev?search_path=public"
  migration {
    dir = "file://ent/migrate/migrations"
  }
}
