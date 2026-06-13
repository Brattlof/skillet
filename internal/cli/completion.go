package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/Brattlof/skillet/internal/install"
	"github.com/Brattlof/skillet/internal/registry"
)

func cmdCompletion(_ context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: skillet completion <bash|zsh|fish>")
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		return fmt.Errorf("unsupported shell %q (use bash, zsh, or fish)", args[0])
	}
	return nil
}

// cmdComplete is the hidden helper the generated completion scripts call to list
// candidate skill names. It reads only the local cache or embedded index, so it
// never blocks tab completion on the network.
func cmdComplete(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("__complete", flag.ContinueOnError)
	dir := fs.String("dir", "", dirUsage)
	tgtFlag := fs.String("target", "", targetUsage)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	tgt, err := resolveTarget(*tgtFlag)
	if err != nil {
		return nil // completion is best-effort
	}
	what := ""
	if len(pos) > 0 {
		what = pos[0]
	}

	switch what {
	case "add", "install", "info", "show", "search", "find":
		entries, err := registry.Cached()
		if err != nil {
			return nil // completion is best-effort
		}
		for _, e := range entries {
			fmt.Println(e.Name)
		}
	case "remove", "rm", "update", "upgrade":
		dirs, err := install.ScanDirs(tgt, *dir)
		if err != nil {
			return nil
		}
		seen := map[string]bool{}
		emit := func(name string) {
			if !seen[name] {
				seen[name] = true
				fmt.Println(name)
			}
		}
		for _, dk := range dirs {
			if recs, err := install.Records(dk.Dir); err == nil {
				for _, r := range recs {
					emit(r.Name)
				}
			}
			// Also offer hand-placed skill directories, whose directory name is
			// the removable name. Command and hook files are not added, since
			// their filename is not the name skillet removes them by.
			if dk.Kind == "skill" {
				if names, err := install.ListInstalled(dk.Dir, dk.Kind); err == nil {
					for _, n := range names {
						emit(n)
					}
				}
			}
		}
	}
	return nil
}

const bashCompletion = `_skillet() {
    local cur prev sub cmds
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    cmds="add install update doctor remove list lock search info registry publish completion self-update version help"

    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "$cmds" -- "$cur") )
        return
    fi
    if [ "$prev" = "--dir" ]; then
        COMPREPLY=( $(compgen -d -- "$cur") )
        return
    fi
    if [ "$prev" = "--target" ]; then
        COMPREPLY=( $(compgen -W "claude agents cursor windsurf gemini cline" -- "$cur") )
        return
    fi

    sub="${COMP_WORDS[1]}"
    case "$sub" in
        add|install|info|search)
            COMPREPLY=( $(compgen -W "$(skillet __complete add 2>/dev/null) --dir --target" -- "$cur") ) ;;
        remove|update)
            COMPREPLY=( $(compgen -W "$(skillet __complete remove 2>/dev/null) --dir --target" -- "$cur") ) ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") ) ;;
        *)
            COMPREPLY=( $(compgen -W "--dir --target" -- "$cur") ) ;;
    esac
}
complete -F _skillet skillet
`

const zshCompletion = `#compdef skillet
_skillet() {
    local -a cmds
    cmds=(add install update doctor remove list lock search info registry publish completion self-update version help)

    if (( CURRENT == 2 )); then
        compadd -- $cmds
        return
    fi

    if [[ ${words[CURRENT-1]} == --target ]]; then
        compadd -- claude agents cursor windsurf gemini cline
        return
    fi

    local sub=${words[2]}
    case $sub in
        add|install|info|search) compadd -- ${(f)"$(skillet __complete add 2>/dev/null)"} ;;
        remove|update)   compadd -- ${(f)"$(skillet __complete remove 2>/dev/null)"} ;;
        completion)      compadd -- bash zsh fish ;;
    esac
    compadd -- --dir --target
}
compdef _skillet skillet
`

const fishCompletion = `complete -c skillet -f
complete -c skillet -n __fish_use_subcommand -a "add install update doctor remove list lock search info registry publish completion self-update version help"
complete -c skillet -n "__fish_seen_subcommand_from add install info search" -a "(skillet __complete add 2>/dev/null)"
complete -c skillet -n "__fish_seen_subcommand_from remove update" -a "(skillet __complete remove 2>/dev/null)"
complete -c skillet -n "__fish_seen_subcommand_from completion" -a "bash zsh fish"
complete -c skillet -l dir -d "Exact install directory"
complete -c skillet -l target -x -a "claude agents cursor windsurf gemini cline" -d "Tool target"
`
