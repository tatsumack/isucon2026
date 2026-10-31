package isureal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
)

const extractBatchSize = 200

// ExtractInitialImages は、初期データの画像を DB から ImageDir/initial/ に書き出す (image_store.go)。
// asia1 で1回だけ実行する (cmd/extract-images)。すでにあるファイルは読み直さないので、途中で止めても続きから再開できる。
// 書き出す前でも、画像は DB から読まれ、読んだときにファイルが埋まるため、アプリは正しく動く。
func ExtractInitialImages(ctx context.Context, config *Config, workers int, log io.Writer) error {
	db, err := sqlx.Open("pgx", config.DatabaseDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(workers)
	store := &ImageStore{dir: config.ImageDir, db: db}

	var partitions []string
	if err := db.SelectContext(ctx, &partitions, `
   SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
   WHERE i.inhparent = 'image_objects'::regclass ORDER BY c.relname
  `); err != nil {
		return err
	}

	var files, bytes atomic.Int64
	start := time.Now()
	jobs := make(chan string)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for partition := range jobs {
				n, size, err := extractPartition(ctx, db, store, partition)
				if err != nil {
					errs <- fmt.Errorf("%s: %w", partition, err)
					return
				}
				files.Add(n)
				bytes.Add(size)
				elapsed := time.Since(start).Seconds()
				fmt.Fprintf(log, "%s: %d files, %d MB (total %d files, %d MB, %.1f MB/s)\n",
					partition, n, size>>20, files.Load(), bytes.Load()>>20, float64(bytes.Load()>>20)/elapsed)
			}
		}()
	}
	for _, partition := range partitions {
		select {
		case jobs <- partition:
		case err := <-errs:
			close(jobs)
			wg.Wait()
			return err
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
	}
	// ファイルごとに fsync しない代わりに、最後にまとめてディスクへ書き出す
	if err := exec.CommandContext(ctx, "sync").Run(); err != nil {
		return err
	}
	fmt.Fprintf(log, "done: %d files, %d MB in %s\n", files.Load(), bytes.Load()>>20, time.Since(start).Round(time.Second))
	return nil
}

func extractPartition(ctx context.Context, db *sqlx.DB, store *ImageStore, partition string) (int64, int64, error) {
	var ids []string
	if err := db.SelectContext(ctx, &ids, fmt.Sprintf(`
   SELECT id FROM %q WHERE created_at <= (SELECT created_at FROM initial_data_boundary) AND octet_length(data) > 0
  `, partition)); err != nil {
		return 0, 0, err
	}
	missing := ids[:0]
	for _, id := range ids {
		if _, ok := readImageFileSize(store.path("initial", id)); !ok {
			missing = append(missing, id)
		}
	}
	var files, bytes int64
	for len(missing) > 0 {
		batch := missing[:min(extractBatchSize, len(missing))]
		missing = missing[len(batch):]
		var rows []struct {
			ID   string `db:"id"`
			Data []byte `db:"data"`
		}
		if err := db.SelectContext(ctx, &rows, fmt.Sprintf(`SELECT id, data FROM %q WHERE id = ANY($1)`, partition), batch); err != nil {
			return files, bytes, err
		}
		for _, row := range rows {
			if err := writeFileAtomic(store.path("initial", row.ID), row.Data, false); err != nil {
				return files, bytes, err
			}
			files++
			bytes += int64(len(row.Data))
		}
	}
	return files, bytes, nil
}

func readImageFileSize(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return 0, false
	}
	return info.Size(), true
}
