package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"db_inner_migrator_syncer/internal/config"
	"db_inner_migrator_syncer/internal/db"
	"db_inner_migrator_syncer/internal/diff"
	"db_inner_migrator_syncer/internal/migrate"
	"db_inner_migrator_syncer/internal/server"
	"db_inner_migrator_syncer/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "init-config":
		if err := initConfigCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "diff":
		if err := diffCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "store-script":
		if err := storeScriptCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "list-scripts":
		if err := listScriptsCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "apply":
		if err := applyCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "status":
		if err := statusCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "rollback":
		if err := rollbackCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "serve":
		if err := serveCmd(args); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`db_inner_migrator_syncer commands:
  init-config   - create a starter config.yaml
  diff          - compare staging and production schemas
  store-script  - copy forward/rollback SQL into storage
  list-scripts  - list stored migration scripts for a pair
  apply         - run migration on staging then production
  rollback      - execute a stored rollback on one environment
  status        - show recent migration status entries
  serve         - launch web UI + JSON API

Flags are command specific; run "<cmd> -h" for details.`)
}

func initConfigCmd(args []string) error {
	fs := flagSet("init-config")
	path := fs.String("path", "config.yaml", "where to write the sample config")
	storagePath := fs.String("storage", "./storage", "local storage root for scripts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := os.Stat(*path); err == nil {
		return fmt.Errorf("%s already exists", *path)
	}

	content := fmt.Sprintf(`storage:
  path: %s
pairs:
  - name: default
    migration_table: migration_status
    staging:
      provider: postgres
      dsn: postgres://user:password@staging-host:5432/database?sslmode=disable
      schema: public
    production:
      provider: mysql
      dsn: user:password@tcp(prod-host:3306)/database?parseTime=true
`, *storagePath)
	if err := os.WriteFile(*path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Println("sample config written to", *path)
	return nil
}

func diffCmd(args []string) error {
	fs := flagSet("diff")
	configPath := fs.String("config", "config.yaml", "path to config file")
	pairName := fs.String("pair", "default", "pair name from config")
	stagingSchema := fs.String("staging-schema", "", "override staging schema")
	productionSchema := fs.String("production-schema", "", "override production schema")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, pair, err := loadPair(*configPath, *pairName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stagingAdapter, err := db.Open(pair.Staging)
	if err != nil {
		return err
	}
	defer stagingAdapter.Close()

	prodAdapter, err := db.Open(pair.Production)
	if err != nil {
		return err
	}
	defer prodAdapter.Close()

	stSchema := pair.Staging.Schema
	if *stagingSchema != "" {
		stSchema = *stagingSchema
	}
	prSchema := pair.Production.Schema
	if *productionSchema != "" {
		prSchema = *productionSchema
	}

	stagingSchemaMeta, err := stagingAdapter.FetchSchema(ctx, stSchema)
	if err != nil {
		return fmt.Errorf("staging schema: %w", err)
	}
	prodSchemaMeta, err := prodAdapter.FetchSchema(ctx, prSchema)
	if err != nil {
		return fmt.Errorf("production schema: %w", err)
	}

	d := diff.Compare(stagingSchemaMeta, prodSchemaMeta)
	fmt.Println(diff.Describe(d))
	return nil
}

func storeScriptCmd(args []string) error {
	fs := flagSet("store-script")
	configPath := fs.String("config", "config.yaml", "path to config file")
	pairName := fs.String("pair", "default", "pair name from config")
	name := fs.String("name", "", "migration name")
	forward := fs.String("script", "", "path to forward SQL file")
	rollback := fs.String("rollback", "", "path to rollback SQL file (optional)")
	desc := fs.String("description", "", "short description")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *forward == "" {
		return fmt.Errorf("--name and --script are required")
	}
	cfg, pair, err := loadPair(*configPath, *pairName)
	if err != nil {
		return err
	}
	if err := storage.EnsureBase(cfg.StoragePath()); err != nil {
		return err
	}
	record, err := storage.StoreScript(cfg.StoragePath(), pair.Name, *name, *forward, *rollback, *desc)
	if err != nil {
		return err
	}
	fmt.Printf("Stored migration %s for pair %s at %s\n", record.Name, pair.Name, filepath.Dir(record.ForwardFile))
	return nil
}

func listScriptsCmd(args []string) error {
	fs := flagSet("list-scripts")
	configPath := fs.String("config", "config.yaml", "path to config file")
	pairName := fs.String("pair", "default", "pair name from config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, pair, err := loadPair(*configPath, *pairName)
	if err != nil {
		return err
	}
	names, err := storage.ListScripts(cfg.StoragePath(), pair.Name)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("no scripts stored for pair", pair.Name)
		return nil
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

func applyCmd(args []string) error {
	fs := flagSet("apply")
	configPath := fs.String("config", "config.yaml", "path to config file")
	pairName := fs.String("pair", "default", "pair name from config")
	name := fs.String("name", "", "migration name")
	forward := fs.String("script", "", "path to forward SQL; if provided, it will be stored before applying")
	rollback := fs.String("rollback", "", "path to rollback SQL")
	desc := fs.String("description", "", "description used only when storing a new script")
	autoRollback := fs.Bool("auto-rollback", false, "run rollback automatically if forward script fails")
	approve := fs.Bool("approve", false, "skip approval prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name is required")
	}
	cfg, pair, err := loadPair(*configPath, *pairName)
	if err != nil {
		return err
	}
	if err := storage.EnsureBase(cfg.StoragePath()); err != nil {
		return err
	}

	var forwardSQL, rollbackSQL, forwardPath, rollbackPath string
	if *forward != "" {
		record, err := storage.StoreScript(cfg.StoragePath(), pair.Name, *name, *forward, *rollback, *desc)
		if err != nil {
			return err
		}
		forwardPath = record.ForwardFile
		rollbackPath = record.RollbackFile
		_, forwardSQL, rollbackSQL, err = storage.LoadScript(cfg.StoragePath(), pair.Name, *name)
		if err != nil {
			return err
		}
	} else {
		record, fwd, rb, err := storage.LoadScript(cfg.StoragePath(), pair.Name, *name)
		if err != nil {
			return err
		}
		forwardSQL = fwd
		rollbackSQL = rb
		forwardPath = record.ForwardFile
		rollbackPath = record.RollbackFile
	}

	fmt.Printf("About to apply %s on staging then production for pair %s\n", *name, pair.Name)
	if !*approve {
		if ok, err := promptYes("Type YES to proceed: "); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("aborted by user")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runner := migrate.Runner{Pair: *pair}
	if err := runner.Apply(ctx, *name, forwardSQL, rollbackSQL, forwardPath, rollbackPath, *autoRollback); err != nil {
		return err
	}
	fmt.Println("Migration applied to staging and production.")
	return nil
}

func rollbackCmd(args []string) error {
	fs := flagSet("rollback")
	configPath := fs.String("config", "config.yaml", "path to config file")
	pairName := fs.String("pair", "default", "pair name from config")
	name := fs.String("name", "", "migration name to rollback")
	env := fs.String("env", "staging", "environment to rollback (staging or production)")
	rollback := fs.String("rollback", "", "path to rollback SQL; if omitted, uses stored script")
	approve := fs.Bool("approve", false, "skip approval prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("--name is required")
	}
	cfg, pair, err := loadPair(*configPath, *pairName)
	if err != nil {
		return err
	}

	var rollbackSQL, rollbackPath string
	if *rollback != "" {
		content, err := os.ReadFile(*rollback)
		if err != nil {
			return err
		}
		rollbackSQL = string(content)
		rollbackPath, _ = filepath.Abs(*rollback)
	} else {
		record, _, rb, err := storage.LoadScript(cfg.StoragePath(), pair.Name, *name)
		if err != nil {
			return err
		}
		if rb == "" {
			return fmt.Errorf("no rollback script stored for %s", *name)
		}
		rollbackSQL = rb
		rollbackPath = record.RollbackFile
	}

	fmt.Printf("About to rollback %s on %s for pair %s\n", *name, *env, pair.Name)
	if !*approve {
		if ok, err := promptYes("Type YES to proceed: "); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("aborted by user")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runner := migrate.Runner{Pair: *pair}
	if err := runner.Rollback(ctx, *env, *name, rollbackSQL, rollbackPath); err != nil {
		return err
	}
	fmt.Println("Rollback completed.")
	return nil
}

func statusCmd(args []string) error {
	fs := flagSet("status")
	configPath := fs.String("config", "config.yaml", "path to config file")
	pairName := fs.String("pair", "default", "pair name from config")
	limit := fs.Int("limit", 10, "number of rows to fetch per environment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, pair, err := loadPair(*configPath, *pairName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("Staging:")
	if err := printStatus(ctx, pair.Staging, pair.MigrationTable, *limit); err != nil {
		return fmt.Errorf("staging: %w", err)
	}
	fmt.Println("Production:")
	if err := printStatus(ctx, pair.Production, pair.MigrationTable, *limit); err != nil {
		return fmt.Errorf("production: %w", err)
	}
	return nil
}

func serveCmd(args []string) error {
	fs := flagSet("serve")
	configPath := fs.String("config", "config.yaml", "path to config file")
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	srv, err := server.New(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Web UI listening on %s (pair default: %s)\n", *addr, cfg.Pairs[0].Name)
	return http.ListenAndServe(*addr, srv.Handler())
}

func printStatus(ctx context.Context, cfg config.DBConfig, table string, limit int) error {
	adapter, err := db.Open(cfg)
	if err != nil {
		return err
	}
	defer adapter.Close()
	if err := adapter.EnsureMigrationTable(ctx, table); err != nil {
		return err
	}
	rows, err := adapter.FetchStatuses(ctx, table, limit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("  no entries yet")
		return nil
	}
	for _, r := range rows {
		errText := ""
		if r.Error.Valid {
			errText = " err=" + r.Error.String
		}
		fmt.Printf("  [%s] %s status=%s file=%s checksum=%s%s\n", r.AppliedEnv, r.MigrationName, r.Status, r.ScriptFile, r.Checksum, errText)
	}
	return nil
}

func loadPair(configPath, pairName string) (*config.Config, *config.PairConfig, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	pair, err := cfg.Pair(pairName)
	if err != nil {
		return nil, nil, err
	}
	return cfg, pair, nil
}

func promptYes(prompt string) (bool, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "YES"), nil
}

func flagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	return fs
}
