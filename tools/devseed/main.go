// Command devseed 为本地开发准备测试数据：造几个空的视频文件，建好数据库，
// 写入管理员口令，并注册一个指向测试媒体目录的本地目录账号。
//
// 只在本地开发使用，不参与服务端构建产物。用法见 mise run dev:seed。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"litepan/internal/adminauth"
	"litepan/internal/domain"
	"litepan/internal/store"
	"litepan/pkg/security"

	// 注册 localfs 等驱动，账号初始化时才拿得到驱动实现。
	_ "litepan/drivers"
)

const (
	defaultAdminUser = "admin"
	// 默认口令足够强，避开「管理员口令处于非安全状态」的限制。
	defaultAdminPassword = "DevAdmin-2026!"
	defaultAccountName   = "本地测试目录"
	// realAccountName 指向 tests/rel：从真实 NAS 影视库压缩来的样本，
	// 目录与命名方式更接近真实场景，用于手动验证与集成测试。
	realAccountName = "本地影视库"
)

func main() {
	root := flag.String("root", ".", "仓库根目录")
	adminPassword := flag.String("admin-password", defaultAdminPassword, "首次运行时写入的管理员口令")
	forcePassword := flag.Bool("force-admin-password", false, "即使已有口令也覆盖")
	flag.Parse()

	if err := run(*root, *adminPassword, *forcePassword); err != nil {
		fmt.Fprintln(os.Stderr, "devseed:", err)
		os.Exit(1)
	}
}

func run(root, adminPassword string, forcePassword bool) error {
	ctx := context.Background()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}

	testsDir := filepath.Join(absRoot, "tests")
	mediaDir := filepath.Join(testsDir, "media")
	realDir := filepath.Join(testsDir, "rel")
	dataDir := filepath.Join(testsDir, "data")
	strmDir := filepath.Join(testsDir, "strm")

	if err := seedMediaFiles(mediaDir); err != nil {
		return err
	}
	for _, dir := range []string{dataDir, strmDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s: %w", dir, err)
		}
	}

	db, err := store.Open(ctx, store.Options{Path: filepath.Join(dataDir, "litepan.db")})
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("执行迁移: %w", err)
	}
	s := store.New(db)

	passwordWritten, err := seedAdmin(ctx, s, adminPassword, forcePassword)
	if err != nil {
		return err
	}
	accountID, err := seedLocalAccount(ctx, s, defaultAccountName, mediaDir, 1)
	if err != nil {
		return err
	}
	// 真实样本目录不存在时不报错，只是跳过。
	realAccountID := int64(0)
	if info, err := os.Stat(realDir); err == nil && info.IsDir() {
		realAccountID, err = seedLocalAccount(ctx, s, realAccountName, realDir, 2)
		if err != nil {
			return err
		}
	}

	adminInfo := "admin（沿用库中已有口令）"
	if passwordWritten {
		adminInfo = "admin / " + adminPassword + "（仅本地测试库使用）"
	}
	realInfo := "（无真实样本，跳过）"
	if realAccountID > 0 {
		realInfo = fmt.Sprintf("%s（id=%d，localfs）", realAccountName, realAccountID)
	}
	fmt.Printf("测试数据就绪：\n  媒体目录  %s\n  真实样本  %s\n  数据目录  %s\n  STRM 目录 %s\n  管理员    %s\n  账号      %s（id=%d，localfs）\n           %s\n",
		mediaDir, realDir, dataDir, strmDir, adminInfo, defaultAccountName, accountID, realInfo)
	return nil
}

// seedAdmin 只在首次运行时写入口令：tests/data 复用真实运行数据时不能覆盖已有口令。
// 返回本次是否写入了口令，供调用方区分「刚初始化」与「沿用已有」。
func seedAdmin(ctx context.Context, s *store.Store, password string, force bool) (bool, error) {
	if _, ok, err := s.Configs.Get(ctx, adminauth.KeyAdminUsername); err != nil {
		return false, err
	} else if !ok {
		if err := s.Configs.Set(ctx, adminauth.KeyAdminUsername, defaultAdminUser); err != nil {
			return false, err
		}
	}
	if _, ok, err := s.Configs.Get(ctx, adminauth.KeyAdminPassword); err != nil {
		return false, err
	} else if ok && !force {
		return false, nil
	}
	password = strings.TrimSpace(password)
	if password == "" {
		return false, fmt.Errorf("管理员口令不能为空")
	}
	if err := s.Configs.Set(ctx, adminauth.KeyAdminPassword, security.HashPassword(password)); err != nil {
		return false, err
	}
	return true, nil
}

// seedLocalAccount 建一个指向 mediaDir 的本地目录账号；已存在就更新路径并重新激活。
// seedLocalAccount 注册（或按名字更新）一个指向本地目录的 localfs 账号。
func seedLocalAccount(ctx context.Context, s *store.Store, name, dir string, sortOrder int) (int64, error) {
	configJSON := fmt.Sprintf(`{"root_path":%q}`, dir)
	accounts, err := s.Accounts.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, acc := range accounts {
		if acc.Name != name {
			continue
		}
		acc.Config = configJSON
		acc.IsActive = true
		if err := s.Accounts.Update(ctx, acc); err != nil {
			return 0, err
		}
		return acc.ID, nil
	}
	return s.Accounts.Create(ctx, &domain.Account{
		Name:       name,
		DriverType: "localfs",
		Config:     configJSON,
		IsActive:   true,
		SortOrder:  sortOrder,
	})
}
