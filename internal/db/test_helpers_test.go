package db

import (
	"database/sql"
	"reflect"
	"strings"
	"sync"

	"gorm.io/gorm/schema"
)

// openRawForTest opens a *sql.DB against dbURL using the pgx driver, for
// ad-hoc information_schema queries in tests.
func openRawForTest(dbURL string) (*sql.DB, error) {
	return sql.Open("pgx", dbURL)
}

// extractColumnNames reads the `gorm` struct tags of a model and returns the
// declared DB column names in field order. Uses GORM's schema.Parse so the
// parsing matches exactly how GORM itself interprets the tags.
func extractColumnNames(model interface{}) []string {
	cache := &sync.Map{}
	s, err := schema.Parse(model, cache, schema.NamingStrategy{})
	if err != nil {
		return manualColumnNames(model)
	}
	cols := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		if f.DBName != "" {
			cols = append(cols, f.DBName)
		}
	}
	return cols
}

// columnNames is the public-facing alias used by alignment tests.
func columnNames(model interface{}) []string {
	return extractColumnNames(model)
}

// manualColumnNames is a fallback tag parser used only if schema.Parse fails.
func manualColumnNames(model interface{}) []string {
	t := reflect.TypeOf(model)
	var cols []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("gorm")
		col := ""
		for _, part := range strings.Split(tag, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "column:") {
				col = strings.TrimPrefix(part, "column:")
				break
			}
		}
		if col != "" {
			cols = append(cols, col)
		}
	}
	return cols
}
