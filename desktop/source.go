package main

import (
	"go/scanner"
	"go/token"
	"strings"
)

func boundedLog(previous, detail string) string {
	log := previous
	if log != "" {
		log += "\n\n"
	}
	log += safeText(detail)
	runes := []rune(log)
	if len(runes) > 16000 {
		log = "… Earlier activity omitted\n" + string(runes[len(runes)-16000:])
	}
	return log
}

func (g *desktop) decorateSource(path, source string) {
	if path == "Working tree changes" {
		g.decorateDiff(source)
		return
	}
	if !strings.HasSuffix(path, ".go") {
		return
	}
	files := token.NewFileSet()
	file := files.AddFile(path, -1, len(source))
	var scan scanner.Scanner
	scan.Init(file, []byte(source), func(token.Position, string) {}, scanner.ScanComments)
	for i := 0; i < 5000; i++ {
		pos, tok, literal := scan.Scan()
		if tok == token.EOF {
			return
		}
		color := uint32(0)
		switch {
		case tok.IsKeyword():
			color = violet
		case tok == token.STRING || tok == token.CHAR:
			color = cyan
		case tok == token.COMMENT:
			color = muted
		case tok == token.INT || tok == token.FLOAT:
			color = 0xe8ba85ff
		}
		if color == 0 {
			continue
		}
		if literal == "" {
			literal = tok.String()
		}
		start := file.Offset(pos)
		g.preview.DecorateText(start, start+len(literal), themeColor(color), 0, 0)
	}
}

func (g *desktop) decorateDiff(source string) {
	offset := 0
	for _, line := range strings.SplitAfter(source, "\n") {
		color := uint32(0)
		switch {
		case strings.HasPrefix(line, "+"):
			color = cyan
		case strings.HasPrefix(line, "-"):
			color = 0xf29c9cff
		case strings.HasPrefix(line, "@@"):
			color = violet
		}
		if color != 0 {
			g.preview.DecorateText(offset, offset+len(line), themeColor(color), 0, 0)
		}
		offset += len(line)
	}
}
