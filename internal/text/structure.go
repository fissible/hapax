package text

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
	"golang.org/x/text/unicode/norm"
)

// StructureVersion identifies the structural admission policy independently
// from the token contract consumed by feature extraction.
const StructureVersion = "structure-v1"

// StructureOptions controls policy decisions that can be reapplied to a tree.
type StructureOptions struct {
	IncludeBlockQuotes   bool
	MinSententialWords   int
	MinUnpunctuatedWords int
}

// DefaultStructureOptions returns the declared v1 structural policy.
func DefaultStructureOptions() StructureOptions {
	return StructureOptions{MinSententialWords: 4, MinUnpunctuatedWords: 8}
}

// Kind separates structural nodes from feature-bearing text runs.
type Kind string

const (
	KindContainer Kind = "container"
	KindLeaf      Kind = "leaf"
)

func Kinds() []Kind { return []Kind{KindContainer, KindLeaf} }

// ContainerKind identifies Markdown structure that can enclose a text run.
type ContainerKind string

const (
	ContainerDocument        ContainerKind = "document"
	ContainerBlockQuote      ContainerKind = "block-quote"
	ContainerList            ContainerKind = "list"
	ContainerListItem        ContainerKind = "list-item"
	ContainerTable           ContainerKind = "table"
	ContainerFootnoteSection ContainerKind = "footnote-section"
	ContainerFootnote        ContainerKind = "footnote"
	ContainerDefinitionList  ContainerKind = "definition-list"
)

// Role identifies the source construct represented by a leaf.
type Role string

const (
	RoleParagraph             Role = "paragraph"
	RoleHeading               Role = "heading"
	RoleCodeBlock             Role = "code-block"
	RoleFrontMatter           Role = "front-matter"
	RoleTableCell             Role = "table-cell"
	RoleFootnote              Role = "footnote"
	RoleCaption               Role = "caption"
	RoleImage                 Role = "image"
	RoleHTMLBlock             Role = "html-block"
	RoleDefinitionTerm        Role = "definition-term"
	RoleDefinitionDescription Role = "definition-description"
)

func Roles() []Role {
	return []Role{RoleParagraph, RoleHeading, RoleCodeBlock, RoleFrontMatter, RoleTableCell, RoleFootnote, RoleCaption, RoleImage, RoleHTMLBlock, RoleDefinitionTerm, RoleDefinitionDescription}
}

// ExclusionReason records why a leaf is outside the feature population.
type ExclusionReason string

const (
	NotExcluded                ExclusionReason = ""
	ExcludedByRole             ExclusionReason = "excluded-by-role"
	ExcludedNotSentential      ExclusionReason = "excluded-not-sentential"
	ExcludedByBlockQuotePolicy ExclusionReason = "excluded-by-block-quote-policy"
)

func ExclusionReasons() []ExclusionReason {
	return []ExclusionReason{NotExcluded, ExcludedByRole, ExcludedNotSentential, ExcludedByBlockQuotePolicy}
}

// Node is either a structural container or one source text run.
type Node struct {
	Kind         Kind
	Container    ContainerKind
	Role         Role
	Span         Span
	Containers   []ContainerKind
	Children     []*Node
	Excisions    []Span
	Words        int
	EndsTerminal bool
	Sentential   bool
	Included     bool
	Exclusion    ExclusionReason
}

// Leaves returns the tree's text runs in source order.
func (n *Node) Leaves() []*Node {
	if n == nil {
		return nil
	}
	if n.Kind == KindLeaf {
		return []*Node{n}
	}
	var leaves []*Node
	for _, child := range n.Children {
		leaves = append(leaves, child.Leaves()...)
	}
	return leaves
}

// IncludedLeaves returns precisely the runs admitted by the current policy.
func (n *Node) IncludedLeaves() []*Node {
	var included []*Node
	for _, leaf := range n.Leaves() {
		if leaf.Included {
			included = append(included, leaf)
		}
	}
	return included
}

// Reclassify reapplies options using evidence retained on each leaf.
func (n *Node) Reclassify(options StructureOptions) {
	options = structureOptions(options)
	for _, leaf := range n.Leaves() {
		leaf.Sentential = leaf.Words > 0 && ((leaf.EndsTerminal && leaf.Words >= options.MinSententialWords) || leaf.Words >= options.MinUnpunctuatedWords)
		leaf.Exclusion = exclusionFor(leaf, options)
		leaf.Included = leaf.Exclusion == NotExcluded
	}
}

// Structure builds the Markdown tree from raw admitted bytes so parser offsets
// remain valid raw offsets even when NFC changes byte length.
func (d *Document) Structure(options StructureOptions) *Node {
	options = structureOptions(options)
	root := &Node{Kind: KindContainer, Container: ContainerDocument, Span: Span{Length: len(d.raw)}}
	front, start := frontMatter(d.raw)
	if front.Length > 0 {
		root.Children = append(root.Children, d.leaf(RoleFrontMatter, front, nil, []ContainerKind{ContainerDocument}, options))
	}

	source := d.raw[start:]
	markdown := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Footnote, extension.DefinitionList))
	parsed := markdown.Parser().Parse(gmtext.NewReader(source))
	for child := parsed.FirstChild(); child != nil; child = child.NextSibling() {
		root.Children = append(root.Children, d.fromGoldmark(child, source, start, []ContainerKind{ContainerDocument}, options, "")...)
	}
	sortNodes(root.Children)
	return root
}

// RunText resolves a leaf after removing only the explicitly recorded markup.
func (d *Document) RunText(node *Node) (string, error) {
	if node == nil || node.Kind != KindLeaf {
		return "", errors.New("RunText requires a leaf node")
	}
	if _, err := d.Resolve(node.Span); err != nil {
		return "", err
	}
	var out strings.Builder
	at := node.Span.Offset
	for _, excision := range node.Excisions {
		if !validExcision(node.Span, excision, at) {
			return "", ErrSpanOutOfBounds
		}
		out.Write(d.raw[at:excision.Offset])
		at = excision.Offset + excision.Length
	}
	out.Write(d.raw[at : node.Span.Offset+node.Span.Length])
	return normNFC(out.String()), nil
}

// RunTokens returns the document's recorded tokens for one leaf after its
// explicitly recorded markup has been removed. Tokens are never retokenized:
// their classes and spans remain the document-level evidence consumers use.
func (d *Document) RunTokens(node *Node) ([]Token, error) {
	if node == nil || node.Kind != KindLeaf {
		return nil, errors.New("RunTokens requires a leaf node")
	}
	if _, err := d.Resolve(node.Span); err != nil {
		return nil, err
	}

	at := node.Span.Offset
	for _, excision := range node.Excisions {
		if !validExcision(node.Span, excision, at) {
			return nil, ErrSpanOutOfBounds
		}
		at = excision.Offset + excision.Length
	}

	var tokens []Token
	for _, token := range d.runTokenCandidates(node.Span) {
		if spanContains(node.Span, token.Span) && !overlapsAny(token.Span, node.Excisions) {
			tokens = append(tokens, token)
		}
	}
	return tokens, nil
}

// runTokenCandidates bounds a run's scan to tokens beginning within its span.
// Tokens are emitted in ascending raw-byte offset order.
func (d *Document) runTokenCandidates(run Span) []Token {
	tokens := d.cachedTokens()
	first := sort.Search(len(tokens), func(i int) bool {
		return tokens[i].Span.Offset >= run.Offset
	})
	last := sort.Search(len(tokens), func(i int) bool {
		return tokens[i].Span.Offset >= run.Offset+run.Length
	})
	return tokens[first:last]
}

func validExcision(run, excision Span, at int) bool {
	runEnd := run.Offset + run.Length
	return excision.Length > 0 && excision.Offset >= at && excision.Offset <= runEnd && excision.Length <= runEnd-excision.Offset
}

func structureOptions(options StructureOptions) StructureOptions {
	if options == (StructureOptions{}) {
		return DefaultStructureOptions()
	}
	return options
}

func (d *Document) fromGoldmark(n ast.Node, source []byte, base int, path []ContainerKind, options StructureOptions, definitionRole Role) []*Node {
	switch n := n.(type) {
	case *ast.Blockquote:
		return nodes(d.container(ContainerBlockQuote, n, source, base, path, options, ""))
	case *ast.List:
		return nodes(d.container(ContainerList, n, source, base, path, options, ""))
	case *ast.ListItem:
		return nodes(d.container(ContainerListItem, n, source, base, path, options, ""))
	case *extast.Table:
		return nodes(d.table(n, source, base, path, options))
	case *extast.TableHeader, *extast.TableRow:
		return d.transparent(n, source, base, path, options, definitionRole)
	case *extast.TableCell:
		return nodes(d.leaf(RoleTableCell, d.nodeSpan(n, source, base), d.excisions(n, source, base), path, options))
	case *extast.FootnoteList:
		return nodes(d.container(ContainerFootnoteSection, n, source, base, path, options, ""))
	case *extast.Footnote:
		return nodes(d.container(ContainerFootnote, n, source, base, path, options, RoleFootnote))
	case *extast.DefinitionList:
		return nodes(d.container(ContainerDefinitionList, n, source, base, path, options, ""))
	case *extast.DefinitionTerm:
		// Goldmark stores a definition term's source lines on the block itself;
		// unlike a description, it has no paragraph child to traverse.
		return nodes(d.leaf(RoleDefinitionTerm, d.nodeSpan(n, source, base), d.excisions(n, source, base), path, options))
	case *extast.DefinitionDescription:
		return d.transparent(n, source, base, path, options, RoleDefinitionDescription)
	case *ast.Paragraph, *ast.TextBlock:
		role := RoleParagraph
		if definitionRole != "" {
			role = definitionRole
		}
		span := d.nodeSpan(n, source, base)
		if span.Length == 0 {
			return nil
		}
		excisions := d.excisions(n, source, base)
		if role == RoleParagraph && imageOnly(d.raw[span.Offset:span.Offset+span.Length], excisions, span.Offset) {
			role = RoleImage
		}
		return nodes(d.leaf(role, span, excisions, path, options))
	case *ast.Heading:
		span := d.nodeSpan(n, source, base)
		return nodes(d.leaf(RoleHeading, span, d.excisions(n, source, base), path, options))
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		span := d.nodeSpan(n, source, base)
		return nodes(d.leaf(RoleCodeBlock, span, nil, path, options))
	case *ast.HTMLBlock:
		return d.htmlLeafNodes(n, source, base, path, options)
	default:
		return d.transparent(n, source, base, path, options, definitionRole)
	}
}

func (d *Document) table(n *extast.Table, source []byte, base int, path []ContainerKind, options StructureOptions) *Node {
	current := append(append([]ContainerKind(nil), path...), ContainerTable)
	var cells []*Node
	_ = ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if cell, ok := node.(*extast.TableCell); ok {
				cells = append(cells, nodes(d.leaf(RoleTableCell, d.nodeSpan(cell, source, base), d.excisions(cell, source, base), current, options))...)
			}
		}
		return ast.WalkContinue, nil
	})
	if len(cells) == 0 {
		return nil
	}
	sortNodes(cells)
	return &Node{Kind: KindContainer, Container: ContainerTable, Span: enclosing(cells), Children: cells}
}

func (d *Document) container(kind ContainerKind, n ast.Node, source []byte, base int, path []ContainerKind, options StructureOptions, definitionRole Role) *Node {
	current := append(append([]ContainerKind(nil), path...), kind)
	var children []*Node
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		children = append(children, d.fromGoldmark(child, source, base, current, options, definitionRole)...)
	}
	sortNodes(children)
	if len(children) == 0 {
		return nil
	}
	return &Node{Kind: KindContainer, Container: kind, Span: enclosing(children), Children: children}
}

func (d *Document) transparent(n ast.Node, source []byte, base int, path []ContainerKind, options StructureOptions, definitionRole Role) []*Node {
	var children []*Node
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		children = append(children, d.fromGoldmark(child, source, base, path, options, definitionRole)...)
	}
	return children
}

func (d *Document) htmlLeafNodes(n *ast.HTMLBlock, source []byte, base int, path []ContainerKind, options StructureOptions) []*Node {
	span := d.nodeSpan(n, source, base)
	raw := d.raw[span.Offset : span.Offset+span.Length]
	lower := bytes.ToLower(raw)
	open := bytes.Index(lower, []byte("<figcaption>"))
	if open < 0 {
		return nodes(d.leaf(RoleHTMLBlock, span, nil, path, options))
	}
	closeStart := open + len("<figcaption>")
	closeRel := bytes.Index(lower[closeStart:], []byte("</figcaption>"))
	if closeRel < 0 {
		return nodes(d.leaf(RoleHTMLBlock, span, nil, path, options))
	}
	captionEnd := closeStart + closeRel
	parts := []*Node{}
	if open > 0 {
		parts = append(parts, nodes(d.leaf(RoleHTMLBlock, Span{Offset: span.Offset, Length: open + len("<figcaption>")}, nil, path, options))...)
	}
	parts = append(parts, nodes(d.leaf(RoleCaption, Span{Offset: span.Offset + closeStart, Length: captionEnd - closeStart}, nil, path, options))...)
	if captionEnd < len(raw) {
		parts = append(parts, nodes(d.leaf(RoleHTMLBlock, Span{Offset: span.Offset + captionEnd, Length: len(raw) - captionEnd}, nil, path, options))...)
	}
	return parts
}

func (d *Document) leaf(role Role, span Span, excisions []Span, path []ContainerKind, options StructureOptions) *Node {
	if span.Length == 0 {
		return nil
	}
	leaf := &Node{Kind: KindLeaf, Role: role, Span: span, Containers: append([]ContainerKind(nil), path...), Excisions: excisions}
	text, err := d.RunText(leaf)
	if err != nil {
		panic("text: structurally invalid leaf: " + err.Error())
	}
	for _, token := range d.runTokenCandidates(span) {
		if token.Lexical && spanContains(span, token.Span) && !overlapsAny(token.Span, excisions) {
			leaf.Words++
		}
	}
	leaf.EndsTerminal = endsTerminal(text)
	leaf.Sentential = leaf.Words > 0 && ((leaf.EndsTerminal && leaf.Words >= options.MinSententialWords) || leaf.Words >= options.MinUnpunctuatedWords)
	leaf.Exclusion = exclusionFor(leaf, options)
	leaf.Included = leaf.Exclusion == NotExcluded
	return leaf
}

func exclusionFor(leaf *Node, options StructureOptions) ExclusionReason {
	switch leaf.Role {
	case RoleHeading, RoleCodeBlock, RoleFrontMatter, RoleTableCell, RoleImage, RoleHTMLBlock, RoleDefinitionTerm:
		return ExcludedByRole
	}
	if leaf.Words == 0 {
		return ExcludedNotSentential
	}
	if containsContainer(leaf.Containers, ContainerListItem) && !leaf.Sentential {
		return ExcludedNotSentential
	}
	if containsContainer(leaf.Containers, ContainerBlockQuote) && !options.IncludeBlockQuotes {
		return ExcludedByBlockQuotePolicy
	}
	return NotExcluded
}

func (d *Document) nodeSpan(n ast.Node, source []byte, base int) Span {
	lines := n.Lines()
	if lines.Len() == 0 {
		return Span{}
	}
	start, stop := lines.At(0).Start, lines.At(0).Stop
	for i := 1; i < lines.Len(); i++ {
		segment := lines.At(i)
		if segment.Start < start {
			start = segment.Start
		}
		if segment.Stop > stop {
			stop = segment.Stop
		}
	}
	for stop > start && (source[stop-1] == '\n' || source[stop-1] == '\r') {
		stop--
	}
	if stop == start {
		return Span{}
	}
	return d.Snap(Span{Offset: base + start, Length: stop - start})
}

func (d *Document) excisions(n ast.Node, source []byte, base int) []Span {
	span := d.nodeSpan(n, source, base)
	found := lineGapExcisions(n.Lines(), source, base)
	found = append(found, inlineExcisions(n, d.raw, span, base)...)
	return normalizeExcisions(found)
}

// normalizeExcisions returns the sorted union of excisions. Adjacent spans stay
// distinct: only bytes claimed by both sources coalesce.
func normalizeExcisions(found []Span) []Span {
	if len(found) < 2 {
		return found
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Offset < found[j].Offset })

	normalized := found[:0]
	for _, candidate := range found {
		if len(normalized) == 0 {
			normalized = append(normalized, candidate)
			continue
		}

		last := &normalized[len(normalized)-1]
		lastEnd := last.Offset + last.Length
		candidateEnd := candidate.Offset + candidate.Length
		if candidate.Offset < lastEnd {
			if candidateEnd > lastEnd {
				last.Length = candidateEnd - last.Offset
			}
			continue
		}
		normalized = append(normalized, candidate)
	}
	return normalized
}

// lineGapExcisions preserves line breaks while removing bytes the block parser
// excluded from each continuation segment: quote markers and list indentation.
func lineGapExcisions(lines *gmtext.Segments, source []byte, base int) []Span {
	var found []Span
	for i := 1; i < lines.Len(); i++ {
		start, stop := lines.At(i-1).Stop, lines.At(i).Start
		for at := start; at < stop; {
			if source[at] == '\r' || source[at] == '\n' {
				at++
				continue
			}
			end := at + 1
			for end < stop && source[end] != '\r' && source[end] != '\n' {
				end++
			}
			found = append(found, Span{Offset: base + at, Length: end - at})
			at = end
		}
	}
	return found
}

// inlineExcisions removes source syntax only when it can be attributed to the
// corresponding Goldmark node. The small scans locate raw envelopes because
// Goldmark's CodeSpan, Image and FootnoteLink nodes do not retain them.
func inlineExcisions(n ast.Node, raw []byte, span Span, base int) []Span {
	b := raw[span.Offset : span.Offset+span.Length]
	var codes []*ast.CodeSpan
	var images []*ast.Image
	var footnotes int
	_ = ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch node := node.(type) {
			case *ast.CodeSpan:
				codes = append(codes, node)
			case *ast.Image:
				images = append(images, node)
			case *extast.FootnoteLink:
				footnotes++
			}
		}
		return ast.WalkContinue, nil
	})
	var found []Span
	codeCandidates := codeSpanCandidates(b, span.Offset)
	usedCodes := make([]bool, len(codeCandidates))
	for _, code := range codes {
		if candidate, ok := inlineNodeCandidate(code, codeCandidates, usedCodes, base); ok {
			found = append(found, candidate)
		}
	}
	imageCandidates := imageCandidates(b, span.Offset)
	usedImages := make([]bool, len(imageCandidates))
	for _, image := range images {
		if candidate, ok := inlineNodeCandidate(image, imageCandidates, usedImages, base); ok {
			found = append(found, candidate)
		}
	}
	// Empty-alt images retain no text segment from which to anchor their raw
	// envelope.  They are nevertheless unambiguous when every remaining raw
	// image form is accounted for by an Image node.  Otherwise leave all of
	// them intact: source-looking bytes are not evidence enough to delete.
	for _, image := range images {
		if _, _, anchored := inlineTextRange(image, base); anchored {
			continue
		}
		if len(images) != len(imageCandidates) {
			break
		}
		for i, candidate := range imageCandidates {
			if !usedImages[i] {
				usedImages[i] = true
				found = append(found, candidate)
				break
			}
		}
	}
	footnoteCandidates := footnoteCandidates(b, span.Offset)
	resolved := resolvedFootnoteLabels(n)
	for _, candidate := range footnoteCandidates {
		if _, ok := resolved[string(raw[candidate.Offset+2:candidate.Offset+candidate.Length-1])]; !ok {
			continue
		}
		if overlapsAny(candidate, found) {
			continue
		}
		found = append(found, candidate)
	}
	// A mismatch means the raw envelope cannot safely be established.  The
	// resolved node remains represented by its raw source rather than crashing
	// or deleting a lookalike bracket expression.
	_ = footnotes
	return found
}

// inlineNodeCandidate returns the only raw candidate containing every source
// Text segment Goldmark retained for node. A node with no retained source text
// (for example an empty code span or image) has no safe source anchor.
func inlineNodeCandidate(node ast.Node, candidates []Span, used []bool, base int) (Span, bool) {
	start, stop, ok := inlineTextRange(node, base)
	if !ok {
		return Span{}, false
	}
	match := -1
	for i, candidate := range candidates {
		if !used[i] && candidate.Offset < start && stop < candidate.Offset+candidate.Length {
			if match >= 0 {
				return Span{}, false
			}
			match = i
		}
	}
	if match < 0 {
		return Span{}, false
	}
	used[match] = true
	return candidates[match], true
}

func inlineTextRange(node ast.Node, base int) (int, int, bool) {
	start, stop := 0, 0
	found := false
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		text, ok := child.(*ast.Text)
		if !ok || text.Segment.IsEmpty() {
			return ast.WalkContinue, nil
		}
		if !found || text.Segment.Start < start {
			start = text.Segment.Start
		}
		if !found || text.Segment.Stop > stop {
			stop = text.Segment.Stop
		}
		found = true
		return ast.WalkContinue, nil
	})
	return base + start, base + stop, found
}

func resolvedFootnoteLabels(n ast.Node) map[string]struct{} {
	root := n
	for root.Parent() != nil {
		root = root.Parent()
	}
	labels := map[string]struct{}{}
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if footnote, ok := node.(*extast.Footnote); ok && footnote.Index >= 0 {
				labels[string(footnote.Ref)] = struct{}{}
			}
		}
		return ast.WalkContinue, nil
	})
	return labels
}

func countFootnoteExcisions(found, candidates []Span) int {
	count := 0
	for _, candidate := range candidates {
		for _, excision := range found {
			if candidate == excision {
				count++
				break
			}
		}
	}
	return count
}

func codeSpanCandidates(b []byte, base int) []Span {
	var found []Span
	for i := 0; i < len(b); {
		if b[i] == '`' {
			width := 1
			for i+width < len(b) && b[i+width] == '`' {
				width++
			}
			if close := bytes.Index(b[i+width:], bytes.Repeat([]byte{'`'}, width)); close >= 0 {
				end := i + width + close + width
				if end == len(b) || b[end] != '`' {
					found = append(found, Span{Offset: base + i, Length: end - i})
					i = end
					continue
				}
			}
		}
		_, size := utf8.DecodeRune(b[i:])
		i += size
	}
	return found
}

func imageCandidates(b []byte, base int) []Span {
	var found []Span
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '!' && b[i+1] == '[' {
			if end := imageEnvelopeEnd(b, i); end > i {
				found = append(found, Span{Offset: base + i, Length: end - i})
				i = end - 1
			}
		}
	}
	return found
}

// imageEnvelopeEnd recognizes the three CommonMark image suffix forms after
// an alt label: inline, full/collapsed reference, and shortcut reference.
// It is intentionally a locator, not a Markdown parser; parser attribution in
// inlineExcisions decides whether a candidate may be removed.
func imageEnvelopeEnd(b []byte, start int) int {
	altEnd := matchingBracket(b, start+1, '[', ']')
	if altEnd < 0 {
		return -1
	}
	if altEnd == len(b) {
		return altEnd // shortcut reference at end of the run
	}
	switch b[altEnd] {
	case '(':
		return matchingBracket(b, altEnd, '(', ')')
	case '[':
		return matchingBracket(b, altEnd, '[', ']')
	default:
		return altEnd // shortcut reference
	}
}

func matchingBracket(b []byte, start int, open, close byte) int {
	depth := 0
	for i := start; i < len(b); i++ {
		if b[i] == '\\' {
			i++
			continue
		}
		switch b[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func footnoteCandidates(b []byte, base int) []Span {
	var found []Span
	for i := 0; i+2 < len(b); {
		if b[i] == '[' && b[i+1] == '^' {
			if end := bytes.IndexByte(b[i+2:], ']'); end >= 0 {
				end += i + 3
				found = append(found, Span{Offset: base + i, Length: end - i})
				i = end
				continue
			}
		}
		_, size := utf8.DecodeRune(b[i:])
		i += size
	}
	return found
}

func imageOnly(raw []byte, excisions []Span, base int) bool {
	if len(excisions) == 0 {
		return false
	}
	candidates := imageCandidates(raw, base)
	if len(candidates) != len(excisions) {
		return false
	}
	for i := range candidates {
		if candidates[i] != excisions[i] {
			return false
		}
	}
	var rest strings.Builder
	at := 0
	for _, excision := range excisions {
		start := excision.Offset - base
		rest.Write(raw[at:start])
		at = start + excision.Length
	}
	rest.Write(raw[at:])
	return strings.TrimSpace(rest.String()) == ""
}

func endsTerminal(s string) bool {
	s = strings.TrimSpace(s)
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if strings.ContainsRune("\"'”’)]}*_", r) {
			s = s[:len(s)-size]
			continue
		}
		return r == '.' || r == '!' || r == '?'
	}
	return false
}

func containsContainer(path []ContainerKind, want ContainerKind) bool {
	for _, kind := range path {
		if kind == want {
			return true
		}
	}
	return false
}
func nodes(node *Node) []*Node {
	if node == nil {
		return nil
	}
	return []*Node{node}
}
func spanContains(outer, inner Span) bool {
	return inner.Offset >= outer.Offset && inner.Offset+inner.Length <= outer.Offset+outer.Length
}
func overlapsAny(span Span, excisions []Span) bool {
	for _, excision := range excisions {
		if span.Offset < excision.Offset+excision.Length && excision.Offset < span.Offset+span.Length {
			return true
		}
	}
	return false
}
func enclosing(nodes []*Node) Span {
	start, end := nodes[0].Span.Offset, nodes[0].Span.Offset+nodes[0].Span.Length
	for _, node := range nodes[1:] {
		if node.Span.Offset < start {
			start = node.Span.Offset
		}
		if e := node.Span.Offset + node.Span.Length; e > end {
			end = e
		}
	}
	return Span{Offset: start, Length: end - start}
}
func sortNodes(nodes []*Node) {
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Span.Offset < nodes[j].Span.Offset })
}
func normNFC(s string) string { return norm.NFC.String(s) }

func frontMatter(raw []byte) (Span, int) {
	lineEnd := func(at int) (int, int) {
		if at >= len(raw) {
			return at, at
		}
		if n := bytes.IndexByte(raw[at:], '\n'); n >= 0 {
			end := at + n
			next := end + 1
			if end > at && raw[end-1] == '\r' {
				end--
			}
			return end, next
		}
		return len(raw), len(raw)
	}
	first, next := lineEnd(0)
	if !bytes.Equal(raw[:first], []byte("---")) {
		return Span{}, 0
	}
	bodyStart := next
	for at := next; at < len(raw); {
		end, following := lineEnd(at)
		if bytes.Equal(raw[at:end], []byte("---")) {
			bodyEnd := at
			if bodyEnd > bodyStart && raw[bodyEnd-1] == '\n' {
				bodyEnd--
				if bodyEnd > bodyStart && raw[bodyEnd-1] == '\r' {
					bodyEnd--
				}
			}
			return Span{Offset: bodyStart, Length: bodyEnd - bodyStart}, following
		}
		at = following
	}
	return Span{}, 0
}
