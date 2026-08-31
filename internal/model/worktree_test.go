package model

import "testing"

func TestSplitWorktreePath(t *testing.T) {
	cases := []struct {
		name         string
		path         string
		wantBase     string
		wantWorktree string
	}{
		{
			name:         "Windows のワークツリー",
			path:         `C:\Users\me\src\insights\.claude\worktree\feat-x`,
			wantBase:     `C:\Users\me\src\insights`,
			wantWorktree: "feat-x",
		},
		{
			name:         "Unix のワークツリー",
			path:         "/home/me/src/insights/.claude/worktree/feat-x",
			wantBase:     "/home/me/src/insights",
			wantWorktree: "feat-x",
		},
		{
			name:         "worktrees（複数形）も拾う",
			path:         "/home/me/src/insights/.claude/worktrees/feat-x",
			wantBase:     "/home/me/src/insights",
			wantWorktree: "feat-x",
		},
		{
			name:         "ワークツリーのさらに下が cwd でも名前は先頭要素",
			path:         "/home/me/src/insights/.claude/worktree/feat-x/internal/cli",
			wantBase:     "/home/me/src/insights",
			wantWorktree: "feat-x",
		},
		{
			name:         "ワークツリーではない",
			path:         "/home/me/src/insights",
			wantBase:     "/home/me/src/insights",
			wantWorktree: "",
		},
		{
			name:         "同じ .claude でもワークツリーでなければ触らない",
			path:         "/home/me/src/insights/.claude/skills",
			wantBase:     "/home/me/src/insights/.claude/skills",
			wantWorktree: "",
		},
		{
			name:         "名前が無ければワークツリーとみなさない",
			path:         "/home/me/src/insights/.claude/worktree",
			wantBase:     "/home/me/src/insights/.claude/worktree",
			wantWorktree: "",
		},
		{
			name:         "空文字",
			path:         "",
			wantBase:     "",
			wantWorktree: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, worktree := SplitWorktreePath(tc.path)
			if base != tc.wantBase || worktree != tc.wantWorktree {
				t.Errorf("SplitWorktreePath(%q) = (%q, %q), want (%q, %q)",
					tc.path, base, worktree, tc.wantBase, tc.wantWorktree)
			}
		})
	}
}
