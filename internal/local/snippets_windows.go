package local

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Snippet is a reusable shell command template (supports {{var}} placeholders).
type Snippet struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Pinned      bool   `json:"pinned,omitempty"`
}

const snippetsLimit = 2 << 20

func defaultSnippets() []Snippet {
	return []Snippet{
		{ID: "preset-sys-overview", Name: "系统负载与内存", Category: "系统", Command: "uname -a && uptime && free -h", Description: "快速查看内核、运行时间与内存使用", Pinned: true},
		{ID: "preset-disk-usage", Name: "磁盘占用 Top 15", Category: "磁盘", Command: "df -h && du -xh --max-depth=1 {{path}} 2>/dev/null | sort -hr | head -n 15", Description: "定位大目录占用（可指定路径，如 /var）", Pinned: true},
		{ID: "preset-ports", Name: "监听端口一览", Category: "网络", Command: "ss -tulpn", Description: "列出所有正在监听的 TCP/UDP 端口及进程", Pinned: true},
		{ID: "preset-top-mem", Name: "内存占用 Top 10 进程", Category: "系统", Command: "ps aux --sort=-%mem | head -n 11", Description: "按内存使用降序查看前 10 个进程"},
		{ID: "preset-docker-ps", Name: "Docker 容器状态", Category: "Docker", Command: "docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'", Description: "整洁展示运行中的容器、状态与端口映射"},
		{ID: "preset-docker-logs", Name: "跟踪容器日志", Category: "Docker", Command: "docker logs -f --tail 100 {{container}}", Description: "实时查看指定容器最后 100 行日志"},
		{ID: "preset-systemd-logs", Name: "Systemd 服务日志", Category: "服务", Command: "journalctl -u {{service}} -n 100 -f --no-pager", Description: "实时跟踪 systemd 服务的 journal 日志"},
		{ID: "preset-nginx-reload", Name: "校验并重载 Nginx", Category: "服务", Command: "sudo nginx -t && sudo systemctl reload nginx", Description: "先校验配置无误再平滑重载 Nginx"},
	}
}

func normalizeSnippet(s Snippet) (Snippet, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Category = strings.TrimSpace(s.Category)
	s.Command = strings.TrimSpace(s.Command)
	s.Description = strings.TrimSpace(s.Description)
	if s.Category == "" {
		s.Category = "通用"
	}
	if s.Name == "" || utf8.RuneCountInString(s.Name) > 120 {
		return s, errors.New("请填写片段名称（最多 120 字符）")
	}
	if s.Command == "" || utf8.RuneCountInString(s.Command) > 8192 {
		return s, errors.New("请填写命令内容（最多 8192 字符）")
	}
	if strings.IndexFunc(s.Name, unicode.IsControl) >= 0 || strings.IndexFunc(s.Category, unicode.IsControl) >= 0 {
		return s, errors.New("片段名称或分类不能包含控制字符")
	}
	return s, nil
}

func readSnippets(dir string) ([]Snippet, error) {
	data, err := readLimited(filepath.Join(dir, "snippets.json"), snippetsLimit)
	if errors.Is(err, os.ErrNotExist) {
		seed := defaultSnippets()
		_ = writeSnippets(dir, seed)
		return seed, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Snippet
	if !json.Valid(data) || json.NewDecoder(bytes.NewReader(data)).Decode(&list) != nil || list == nil {
		return nil, errors.New("命令片段配置文件格式无效")
	}
	return list, nil
}

func writeSnippets(dir string, list []Snippet) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > snippetsLimit {
		return errors.New("命令片段配置超过 2 MiB 容量")
	}
	return writeAtomic(filepath.Join(dir, "snippets.json"), data)
}

func Snippets() ([]Snippet, error) {
	var list []Snippet
	err := withStore(func(dir string) error {
		var err error
		list, err = readSnippets(dir)
		return err
	})
	return list, err
}

func SaveSnippet(s Snippet) (Snippet, error) {
	s, err := normalizeSnippet(s)
	if err != nil {
		return Snippet{}, err
	}
	err = withStore(func(dir string) error {
		list, err := readSnippets(dir)
		if err != nil {
			return err
		}
		if s.ID == "" {
			id, err := newID()
			if err != nil {
				return err
			}
			s.ID = id
			return writeSnippets(dir, append(list, s))
		}
		for i := range list {
			if list[i].ID == s.ID {
				list[i] = s
				return writeSnippets(dir, list)
			}
		}
		return writeSnippets(dir, append(list, s))
	})
	return s, err
}

func DeleteSnippet(id string) error {
	return withStore(func(dir string) error {
		list, err := readSnippets(dir)
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == id {
				return writeSnippets(dir, append(list[:i], list[i+1:]...))
			}
		}
		return nil
	})
}
