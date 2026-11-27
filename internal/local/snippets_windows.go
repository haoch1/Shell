package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// Keep the original 2 MiB user catalog readable when the additional presets are appended.
const snippetsLimit = (2 << 20) + (16 << 10)
const snippetsCatalogVersion = 2

func originalSnippets() []Snippet {
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

func additionalSnippets() []Snippet {
	return []Snippet{
		{ID: "preset-os-version", Name: "系统发行版与内核版本", Category: "系统", Command: "cat /etc/os-release && uname -r"},
		{ID: "preset-cpu-info", Name: "CPU 架构与核心数", Category: "系统", Command: "lscpu"},
		{ID: "preset-top-cpu", Name: "CPU 占用 Top 10 进程", Category: "系统", Command: "ps -eo pid,comm,pcpu --sort=-pcpu | head -n 11"},
		{ID: "preset-free-memory", Name: "内存与交换空间", Category: "系统", Command: "free -h"},
		{ID: "preset-system-clock", Name: "系统时间与时区", Category: "系统", Command: "date '+%F %T %Z'"},
		{ID: "preset-login-users", Name: "当前登录用户", Category: "系统", Command: "who"},
		{ID: "preset-list-files", Name: "当前目录与隐藏文件", Category: "文件", Command: "ls -lah"},
		{ID: "preset-filesystem-space", Name: "文件系统类型与可用空间", Category: "磁盘", Command: "df -hT"},
		{ID: "preset-inode-usage", Name: "磁盘 inode 使用量", Category: "磁盘", Command: "df -ih"},
		{ID: "preset-block-devices", Name: "磁盘与分区", Category: "磁盘", Command: "lsblk -f"},
		{ID: "preset-mounts", Name: "文件系统挂载点", Category: "磁盘", Command: "findmnt"},
		{ID: "preset-ip-addresses", Name: "网卡与 IP 地址", Category: "网络", Command: "ip -brief address"},
		{ID: "preset-routes", Name: "路由表与默认网关", Category: "网络", Command: "ip route"},
		{ID: "preset-socket-summary", Name: "网络连接统计", Category: "网络", Command: "ss -s"},
		{ID: "preset-failed-units", Name: "启动失败的服务与单元", Category: "服务", Command: "systemctl --failed --no-pager"},
		{ID: "preset-running-services", Name: "正在运行的服务", Category: "服务", Command: "systemctl list-units --type=service --state=running --no-pager"},
		{ID: "preset-boot-errors", Name: "本次启动的最近错误日志", Category: "服务", Command: "journalctl -b -p err -n 50 --no-pager"},
		{ID: "preset-docker-images", Name: "Docker 本地镜像", Category: "Docker", Command: "docker image ls"},
		{ID: "preset-docker-space", Name: "Docker 磁盘占用", Category: "Docker", Command: "docker system df"},
		{ID: "preset-docker-stats", Name: "容器 CPU 与内存用量", Category: "Docker", Command: "docker stats --no-stream"},
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
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return nil, err
	}
	var list []Snippet
	if fresh {
		list = originalSnippets()
	} else if json.Unmarshal(data, &list) != nil || list == nil {
		return nil, errors.New("命令片段配置文件格式无效")
	}
	marker := filepath.Join(dir, "snippets-catalog.version")
	versionData, err := readLimited(marker, 32)
	version := 0
	if err == nil {
		version, err = strconv.Atoi(strings.TrimSpace(string(versionData)))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取命令片段预设版本失败：%w", err)
	}
	if !fresh && version >= snippetsCatalogVersion {
		return list, nil
	}
	ids := make(map[string]bool, len(list))
	commands := make(map[string]bool, len(list))
	for _, s := range list {
		ids[s.ID] = true
		commands[strings.TrimSpace(s.Command)] = true
	}
	oldLen := len(list)
	for _, s := range additionalSnippets() {
		if !ids[s.ID] && !commands[s.Command] {
			list = append(list, s)
		}
	}
	if fresh || len(list) != oldLen {
		if err := writeSnippets(dir, list); err != nil {
			return nil, err
		}
	}
	// Mark only after persisting the array. An interrupted migration can safely retry by ID and command.
	if version < snippetsCatalogVersion {
		if err := writeAtomic(marker, []byte(strconv.Itoa(snippetsCatalogVersion))); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func writeSnippets(dir string, list []Snippet) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > snippetsLimit {
		data, err = json.Marshal(list)
		if err != nil {
			return err
		}
		if len(data) > snippetsLimit {
			return errors.New("命令片段配置超过容量限制")
		}
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
