// extract-images は、初期データの画像を DB からファイルに書き出す。asia1 で1回だけ実行する。
//
//	cd /home/isucon/webapp/go && go build -o extract-images ./cmd/extract-images
//	set -a; . /home/isucon/env.sh; set +a; nohup ./extract-images > /home/isucon/extract-images.log 2>&1 &
package main

import (
	"context"
	"flag"
	"os"

	"example.com/isureal-company/isureal"
)

func main() {
	workers := flag.Int("workers", 4, "number of partitions to extract in parallel")
	flag.Parse()
	if err := isureal.ExtractInitialImages(context.Background(), isureal.ConfigFromEnv(), *workers, os.Stdout); err != nil {
		panic(err)
	}
}
