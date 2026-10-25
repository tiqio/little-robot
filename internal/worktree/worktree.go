package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Package worktree用git worktree给每个任务开一个独立目录+独立分支
// - worktree共享同一份.git对象库，磁盘最多一个工作目录
// - 各自在自己分支上，能diff、能merge、能干净地丢弃

// 只允许这些字符：既防止路径穿越（../），也防止shell元字符
var validName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Manager 管理一个仓库下所有worktree；所有路径都以它为根，模型碰不到仓库外
type Manager struct {
	repoRoot string // git rev-parse --show-toplevel
	baseDir  string // <repoRoot>/.worktrees
}

// Entry 是git worktree list的一行
type Entry struct {
	Name   string // 从分支名wt/<name>反推
	Path   string
	Branch string
}

func NewManager(dir string) (*Manager, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Manager{
		repoRoot: root,
		baseDir:  filepath.Join(root, ".worktrees"),
	}, nil
}

// RepoRoot 找到dir所属仓库的根目录；不是git仓库就报错
// dd@MacBook-Pro-2 little-robot % git rev-parse --show-toplevel
// /Users/dd/AITEST/little-robot
func RepoRoot(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%q不是git仓库：%w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

// Create 建立一个worktree，返回它的绝对路径
func (m *Manager) Create(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.baseDir, 0o755); err != nil {
		return "", fmt.Errorf("创建工作区根目录失败：%w", err)
	}

	path := filepath.Join(m.baseDir, name)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("worktree %q已存在：%s", name, path)
	}

	// -b：新分支：分支名带wt/前缀，方便一眼看出是agent建的
	if _, err := runGit(m.repoRoot, "worktree", "add", path, "-b", "wt/"+name, "HEAD"); err != nil {
		return "", fmt.Errorf("创建worktree失败：%w", err)
	}
	if err := m.logEvent("create", name); err != nil {
		return "", err
	}
	return path, nil
}

// List 返回baseDir下的所有worktree
func (m *Manager) List() ([]Entry, error) {
	out, err := runGit(m.repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("列出worktree失败：%w", err)
	}

	var entries []Entry
	var cur Entry
	flush := func() {
		if cur.Path == "" {
			return
		}
		// 只认我们自己建的（在.worktrees下）
		if strings.HasPrefix(cur.Path, m.baseDir) {
			entries = append(entries, cur)
		}
		cur = Entry{}
	}

	// dd@MacBook-Pro-2 little-robot % git worktree list --porcelain
	// worktree /Users/dd/AITEST/little-robot
	// HEAD d8726f6f3d21ead8c0a02abfd3a1e964901f7372
	// branch refs/heads/main
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
			cur.Name = strings.TrimPrefix(cur.Branch, "wt/")
		}
	}

	flush()
	return entries, nil
}

// Remove 删掉worktree和它的分支，有未提交改动时默认拒绝——防止把两个小时的话一键清掉
func (m *Manager) Remove(name string, discard bool) error {
	if err := validateName(name); err != nil {
		return err
	}
	path := filepath.Join(m.baseDir, name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("worktree %q不存在：%w", name, err)
	}

	dirty, err := m.hasChanges(path) // dirty=true时代表当前分支有未提交变更
	if err != nil {
		return err
	}
	if dirty && !discard { // 同时discard=true时会清理，否则保留
		return fmt.Errorf("worktree %q还有未提交改动；如果要清理就配置discard为true", name)
	}

	if _, err := runGit(m.repoRoot, "worktree", "remove", path, "--force"); err != nil {
		return fmt.Errorf("删除worktree失败：%w", err)
	}
	// 分支可能删除失败（比如还没被哪些提交引用），不当成知名错误，worktree remove只删除目录，分支留着
	// git worktree remove语义是解除“这个目录占用这个分支”，不是“删掉这个分支”，因为分支是数据，目录只是视图
	// 分支 wt/alice  →  一堆 commit（共享在 .git/objects 里）
	// 目录 wt-alice  →  只是把这些 commit 检出到磁盘
	_, _ = runGit(m.repoRoot, "branch", "-D", "wt/"+name)

	return m.logEvent("remove", name)
}

// Keep 只是记录一条事件，目录和分支都留着，等人review后自己merge
func (m *Manager) Keep(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return m.logEvent("keep", name)
}

// hasChanges 判断有没有“值得保留”保留的东西：未提交改动，或相对主分支有提交
func (m *Manager) hasChanges(path string) (bool, error) {
	status, err := runGit(path, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("检查改动失败：%w", err)
	}
	if strings.TrimSpace(status) != "" {
		return true, nil
	}

	// 有提交但没合并：HEAD上不属于main的提交数 > 0
	count, err := runGit(path, "rev-list", "--count", "main..HEAD")
	if err != nil {
		// 仓库里可能没有main（比如用master），这是只按工作区改动判断
		/*
			在 wt-alice（独立分支）里：
			  git rev-list --count main..HEAD  →  1     ← 有 1 个提交不在 main 上
			  git log --oneline main..HEAD     →  67e7032 alice: change app

			在主仓库 main 上：
			  git rev-list --count main..HEAD  →  0     ← 自己跟自己，永远是 0
		*/
		return false, nil
	}
	return strings.TrimSpace(count) != "0", nil
}

func (m *Manager) logEvent(kind, name string) error {
	ev := map[string]any{
		"kind": kind,
		"name": name,
		"ts":   time.Now().UTC().Format(time.RFC3339),
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("序列化事件失败：%w", err)
	}

	f, err := os.OpenFile(filepath.Join(m.baseDir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开事件日志失败：%w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("写事件日志失败：%w", err)
	}
	return nil
}

func validateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("worktree名字%q不合法：只允许字母/数字/._-，长度1～64", name)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("worktree名字%q不合法：不允许路径穿越", name)
	}
	return nil
}

// runGit 统一入口：dir决定在哪个工作目录执行，错误里带stderr
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w（%s）", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
