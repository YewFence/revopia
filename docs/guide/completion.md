# Shell 补全

`revopia` 使用 Cobra 原生补全能力，不需要额外生成工具。

## 生成补全脚本

构建二进制后，可以通过 `completion` 子命令生成 Bash、Zsh、Fish 和 PowerShell 的补全脚本。

```bash
mise run build
./bin/revopia completion zsh > _revopia
./bin/revopia completion bash > revopia.bash
./bin/revopia completion fish > revopia.fish
./bin/revopia completion powershell > revopia.ps1
```

## Zsh

Zsh 可以把生成的 `_revopia` 放到 `$fpath` 中已有的目录，也可以放到自定义目录后在 `~/.zshrc` 里加入该目录。

```bash
mkdir -p ~/.zsh/completions
./bin/revopia completion zsh > ~/.zsh/completions/_revopia
```

```zsh
fpath=(~/.zsh/completions $fpath)
autoload -Uz compinit
compinit
```

## Bash

Bash 可以把补全脚本放到本地目录后手动 `source`，也可以交给系统的 bash-completion 目录管理。

```bash
mkdir -p ~/.bash_completion.d
./bin/revopia completion bash > ~/.bash_completion.d/revopia.bash
source ~/.bash_completion.d/revopia.bash
```

## Fish

Fish 可以直接写入用户补全目录。

```bash
mkdir -p ~/.config/fish/completions
./bin/revopia completion fish > ~/.config/fish/completions/revopia.fish
```
