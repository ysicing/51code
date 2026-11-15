// Copyright 2026 ysicing
// SPDX-License-Identifier: AGPL-3.0-only

package aikeychain

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

func TestLegacyKeychainMyGoCompatibility(t *testing.T) {
	if os.Getenv("XSOURCETREE_TEST_KEYCHAIN") != "1" {
		t.Skip("仅在显式启用时使用临时系统凭证记录")
	}
	store := Store{Service: fmt.Sprintf("com.ysicing.xsourcetree.test.compat.%d", time.Now().UnixNano())}
	secrets := mygo.Secrets.Service(store.Service)
	const account = "synthetic-account"
	t.Cleanup(func() {
		if err := store.Delete(account); err != nil {
			t.Error(err)
		}
	})
	for _, value := range []string{"legacy-synthetic-key", strings.Repeat("x", mygo.MaxSecretSize+1)} {
		if err := store.Save(account, value); err != nil {
			t.Fatal(err)
		}
		saved, err := secrets.Get(account)
		if err != nil || string(saved) != value {
			t.Fatalf("MyGo 未能读取旧钥匙串记录: %v", err)
		}
	}
	if err := secrets.Set(account, []byte("mygo-synthetic-key")); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(account)
	if err != nil || saved != "mygo-synthetic-key" {
		t.Fatalf("旧实现未能读取 MyGo 写入的记录: %v", err)
	}
	if err := secrets.Delete(account); err != nil {
		t.Fatal(err)
	}
	if saved, err := store.Load(account); err != nil || saved != "" {
		t.Fatalf("MyGo 删除后仍能读取记录: %v", err)
	}
	if err := store.Save(account, "another-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(account); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(account); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatalf("旧实现删除后 MyGo 未返回不存在: %v", err)
	}
}
