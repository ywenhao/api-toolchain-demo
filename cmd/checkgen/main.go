// checkgen 不依赖 Git，也能检查已保存的生成产物是否与协议同步。
package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
)

func snapshot() (map[string][32]byte, error) {
	result := map[string][32]byte{}
	for _, root := range []string{"gen", "openapi"} {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256(data)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	before, err := snapshot()
	if err != nil {
		return err
	}
	for _, args := range [][]string{{"buf", "generate"}, {"buf", "generate", "--template", "buf.gen.openapi-user.yaml", "--path", "api/demo/user/v1"}, {"buf", "generate", "--template", "buf.gen.openapi-admin.yaml", "--path", "api/demo/admin/v1"}, {"go", "run", "./cmd/openapi"}} {
		command := exec.Command(args[0], args[1:]...)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return err
		}
	}
	after, err := snapshot()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("生成产物发生变化，请检查并提交 gen/ 和 openapi/ 中的新产物")
	}
	fmt.Printf("生成产物可复现：%d 个文件逐字节一致\n", len(after))
	return nil
}
