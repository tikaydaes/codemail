package mailmsg

import (
	"bytes"
	"regexp"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/tikaydaes/codemail/internal/extract"
)

// Markdown renders a message body as Markdown, the one format every
// client displays. HTML mail is reduced to its text structure: headings,
// paragraphs, emphasis, lists and links. Scripts, styles, images, hidden
// text and layout tables are dropped, so nothing in the result can load
// remote content or run code. Plain-text mail is escaped so it reads the
// same as it was written.
func Markdown(in extract.Input) string {
	if strings.TrimSpace(in.HTML) != "" {
		if md, ok := htmlToMarkdown(in.HTML); ok {
			return md
		}
	}
	return textToMarkdown(in.Text)
}

var (
	hiddenStyleRe = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden|max-height\s*:\s*0(?:px)?\s*(?:;|$)|font-size\s*:\s*0(?:px)?\s*(?:;|$)|opacity\s*:\s*0\s*(?:;|$)`)
	blankRunRe    = regexp.MustCompile(`\n{3,}`)
	emptyLinkRe   = regexp.MustCompile(`\[\s*\]\([^)\n]*\)`) // icon links whose image was dropped
	commentRe     = regexp.MustCompile(`(?s)<!--.*?-->`)     // comments inside raw-text elements
	// invisibles are characters senders pad preview text with.
	invisibles = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\u034f", "", "\ufeff", "", "\u00ad", "", "\u2007", " ", "\u00a0", " ")
)

// dropped elements never contribute readable text.
var dropped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true, atom.Meta: true,
	atom.Link: true, atom.Svg: true, atom.Iframe: true, atom.Object: true, atom.Embed: true,
	atom.Input: true, atom.Select: true, atom.Textarea: true, atom.Noscript: true, atom.Template: true,
	atom.Video: true, atom.Audio: true, atom.Canvas: true, atom.Map: true,
}

// unwrapped elements are table markup, which email uses for layout. Each
// becomes a plain block so its content reads top to bottom.
var unwrapped = map[atom.Atom]bool{
	atom.Table: true, atom.Thead: true, atom.Tbody: true, atom.Tfoot: true,
	atom.Tr: true, atom.Td: true, atom.Th: true, atom.Caption: true,
}

func htmlToMarkdown(doc string) (string, bool) {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return "", false
	}
	clean(root, false)
	var buf bytes.Buffer
	if err := html.Render(&buf, root); err != nil {
		return "", false
	}
	md, err := htmltomarkdown.ConvertString(buf.String())
	if err != nil {
		return "", false
	}
	md = emptyLinkRe.ReplaceAllString(commentRe.ReplaceAllString(invisibles.Replace(md), ""), "")
	md = strings.TrimSpace(blankRunRe.ReplaceAllString(md, "\n\n"))
	return md, md != ""
}

// clean edits the tree in place. inLink is true below an <a>.
func clean(n *html.Node, inLink bool) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case c.Type == html.CommentNode, c.Type == html.ElementNode && (dropped[c.DataAtom] || hidden(c)):
			n.RemoveChild(c)
		case c.Type == html.ElementNode && c.DataAtom == atom.Img:
			// Inside a link, an image is replaced by its description, which
			// keeps the label of buttons drawn as pictures. Elsewhere
			// images are decoration and are dropped.
			if alt := strings.TrimSpace(attr(c, "alt")); inLink && alt != "" && len(alt) < 80 {
				n.InsertBefore(&html.Node{Type: html.TextNode, Data: alt}, c)
			}
			n.RemoveChild(c)
		case c.Type == html.ElementNode:
			if unwrapped[c.DataAtom] {
				c.DataAtom, c.Data, c.Attr = atom.Div, "div", nil
			}
			clean(c, inLink || c.DataAtom == atom.A)
		}
		c = next
	}
}

func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		if a.Key == "hidden" || (a.Key == "style" && hiddenStyleRe.MatchString(a.Val)) {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

var (
	textURLRe    = regexp.MustCompile(`https?://[^\s<>"'\])]+`)
	mdSpecials   = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`, "#", `\#`, "|", `\|`, "~", `\~`)
	listMarkerRe = regexp.MustCompile(`^(\d+)([.)])|^([-+])`)
)

// textToMarkdown escapes a plain-text body so Markdown renders it as
// written: line breaks kept, URLs clickable, nothing read as formatting.
func textToMarkdown(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var paragraphs []string
	var lines []string
	flush := func() {
		if len(lines) > 0 {
			paragraphs = append(paragraphs, strings.Join(lines, "  \n")) // two spaces: a line break
			lines = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(invisibles.Replace(line))
		if line == "" {
			flush()
			continue
		}
		var b strings.Builder
		last := 0
		for _, loc := range textURLRe.FindAllStringIndex(line, -1) {
			u := strings.TrimRight(line[loc[0]:loc[1]], ".,;:!?")
			b.WriteString(mdSpecials.Replace(line[last:loc[0]]))
			b.WriteString("<" + u + ">")
			last = loc[0] + len(u)
		}
		b.WriteString(mdSpecials.Replace(line[last:]))
		// A leading "1." or "-" would start a list.
		lines = append(lines, listMarkerRe.ReplaceAllString(b.String(), `$1\$2$3`))
	}
	flush()
	return strings.Join(paragraphs, "\n\n")
}
