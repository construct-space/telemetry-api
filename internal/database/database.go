package database

import (
	"fmt"
	"log"
	"time"

	"construct/telemetry/internal/config"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB is the single telemetry database handle. Only one DB here (unlike
// oracle which talks to six) — no need for a name-keyed map.
var DB *gorm.DB

func Init(cfg *config.Config) {
	var dsn string
	var dialector gorm.Dialector

	switch cfg.DBDriver {
	case "mysql":
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			cfg.DBUser, cfg.DBPass, cfg.DBHost, cfg.DBPort, cfg.DBName)
		dialector = mysql.Open(dsn)
	case "postgres", "postgresql":
		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
			cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPass, cfg.DBName, cfg.DBSSL)
		dialector = postgres.Open(dsn)
	default:
		log.Fatalf("unsupported DB_DRIVER %q", cfg.DBDriver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("connect %s://%s:%s/%s: %v", cfg.DBDriver, cfg.DBHost, cfg.DBPort, cfg.DBName, err)
	}
	DB = db
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(50)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}
	log.Printf("[telemetry] connected to %s://%s:%s/%s", cfg.DBDriver, cfg.DBHost, cfg.DBPort, cfg.DBName)
}
