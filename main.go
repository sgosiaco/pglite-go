package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

var (
	//go:embed pglite-wasi.tar.gz
	compressed []byte
)

const tests = `

SHOW client_encoding;

CREATE OR REPLACE FUNCTION test_func() RETURNS TEXT AS $$ BEGIN RETURN 'test'; END; $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION addition (entier1 integer, entier2 integer)
RETURNS integer
LANGUAGE plpgsql
IMMUTABLE
AS '
DECLARE
  resultat integer;
BEGIN
  resultat := entier1 + entier2;
  RETURN resultat;
END ' ;

SELECT test_func();

SELECT now(), current_database(), session_user, current_user;

SELECT addition(40,2);

`

func main() {
	// extract the tar if we don't have tmp dir
	blob, err := setupEnv()
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	// setting up dir mounts for r/w
	fsConfig := wazero.NewFSConfig().WithDirMount("./tmp", "/tmp").WithDirMount("./dev", "/dev")

	config := wazero.NewModuleConfig().
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		WithFSConfig(fsConfig)
	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	pglite, err := r.InstantiateWithConfig(
		ctx,
		blob,
		config.
			WithArgs("--single", "postgres").
			WithEnv("ENVIRONMENT", "wasi-embed").
			WithEnv("REPL", "N").
			WithEnv("PGUSER", "postgres").
			WithEnv("PGDATABASE", "postgres"),
	)
	if err != nil {
		// Note: Most compilers do not exit the module after running "_start",
		// unless there was an error. This allows you to call exported functions.
		if exitErr, ok := err.(*sys.ExitError); ok && exitErr.ExitCode() != 0 {
			fmt.Fprintf(os.Stderr, "exit_code: %d\n", exitErr.ExitCode())
		} else if !ok {
			log.Panicln(err)
		}
	}

	initDBRV, err := pglite.ExportedFunction("pg_initdb").Call(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("initdb returned: %b\n", initDBRV)

	_, err = pglite.ExportedFunction("use_socketfile").Call(ctx)
	if err != nil {
		log.Fatal(err)
	}

	query := func(input string) error {
		sqlCstring := append([]byte(input), 0)
		pglite.Memory().Write(1, sqlCstring)

		_, err = pglite.ExportedFunction("interactive_one").Call(ctx)
		return err
	}

	// run tests
	for _, line := range strings.Split(tests, "\n\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			fmt.Println("REPL:", line)
			if err := query(line); err != nil {
				log.Fatal(err)
			}
		}
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		input, err := reader.ReadString(';')
		if err != nil {
			log.Fatal(err)
		}

		if err := query(input); err != nil {
			log.Fatal(err)
		}
	}
}

func setupEnv() ([]byte, error) {
	// check if tar.gz already extracted; if not do so
	if _, err := os.Stat("./tmp/pglite/base/PG_VERSION"); err != nil {
		fmt.Println("Extracting env....")
		gr, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, err
		}
		defer gr.Close()

		tr := tar.NewReader(gr)

		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}

			dest := filepath.Join("./", header.Name)

			switch header.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(dest, os.FileMode(header.Mode)); err != nil {
					return nil, err
				}
			case tar.TypeReg:
				if err := os.MkdirAll(filepath.Dir(dest), os.FileMode(header.Mode)); err != nil {
					return nil, err
				}

				of, err := os.Create(dest)
				if err != nil {
					return nil, err
				}
				defer of.Close()

				if _, err := io.Copy(of, tr); err != nil {
					return nil, err
				}
			case tar.TypeSymlink:
				if err := os.Symlink(header.Linkname, dest); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unknown file type in tar: %c (%s)", header.Typeflag, header.Name)
			}
		}
	}

	// setup random
	if err := os.MkdirAll("./dev", 0755); err != nil {
		return nil, err
	}

	rf, err := os.Create("./dev/urandom")
	if err != nil {
		return nil, err
	}
	defer rf.Close()

	rng := make([]byte, 128)
	if _, err := rand.Read(rng); err != nil {
		return nil, err
	}
	rf.Write(rng)

	// read in wasi blob
	return os.ReadFile("./tmp/pglite/bin/postgres.wasi")
}
