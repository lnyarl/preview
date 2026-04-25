// 이 파일의 책임:
//   - "hub migrate up|down|version" 서브커맨드.
package main

import (
	"fmt"

	"github.com/joho/godotenv"
	sqlitestore "github.com/lnyarl/preview/internal/db/sqlite"
	"github.com/lnyarl/preview/internal/hub"
)

func runMigrate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("migrate requires subcommand: up | down | version")
	}
	_ = godotenv.Load()
	cfg := hub.DefaultConfig()
	logger := hub.NewLogger(cfg.LogLevel)

	switch args[0] {
	case "up":
		applied, err := sqlitestore.MigrateUp(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		fmt.Printf("migrate: applied %d\n", applied)
		logger.Info("migrate_applied", "count", applied)
		return nil
	case "down":
		if err := sqlitestore.MigrateDown(cfg.DatabaseURL); err != nil {
			return err
		}
		fmt.Println("migrate: reverted 1")
		logger.Info("migrate_reverted", "count", 1)
		return nil
	case "version":
		v, dirty, err := sqlitestore.MigrateVersion(cfg.DatabaseURL)
		if err != nil {
			return err
		}
		fmt.Printf("migrate: version=%d dirty=%v\n", v, dirty)
		return nil
	default:
		return fmt.Errorf("unknown migrate subcommand: %s", args[0])
	}
}
