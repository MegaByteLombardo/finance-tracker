package fx

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// Small helpers over golang.org/x/net/html.
//
// They exist because an HTML document is parsed into a tree and the standard
// library ships no selector engine, and because the alternative -- matching the
// page with regular expressions -- is not an option. A regular expression cannot
// tell an attribute from text, breaks on an added space or a reordered
// attribute, and fails by silently matching the wrong thing rather than by
// erroring. For a document that decides what a person's net worth is, a parser
// that fails loudly is the only acceptable kind.
//
// Nothing here is a general-purpose CSS implementation. It is four functions
// covering exactly what the BCV page needs.

// elementByID returns the first element whose id attribute equals id, or nil.
func elementByID(root *html.Node, id string) *html.Node {
	var found *html.Node
	walk(root, func(node *html.Node) bool {
		if node.Type == html.ElementNode && attr(node, "id") == id {
			found = node
			return false
		}
		return true
	})
	return found
}

// firstByClass returns the first element in the subtree carrying the given class
// among its space-separated class list, or nil.
func firstByClass(root *html.Node, class string) *html.Node {
	var found *html.Node
	walk(root, func(node *html.Node) bool {
		if node.Type == html.ElementNode && hasClass(node, class) {
			found = node
			return false
		}
		return true
	})
	return found
}

// singleTextByClass returns the trimmed text of the ONE element in the subtree
// carrying the given class.
//
// It insists on exactly one. Two elements with the same class inside a rate box
// means the page changed shape, and picking either would be a guess about which
// number is the rate -- the kind of guess that produces a plausible wrong
// answer.
func singleTextByClass(root *html.Node, class string) (string, error) {
	var matches []*html.Node
	walk(root, func(node *html.Node) bool {
		if node.Type == html.ElementNode && hasClass(node, class) {
			matches = append(matches, node)
		}
		return true
	})

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no element with class %q", class)
	case 1:
		text := strings.TrimSpace(textOf(matches[0]))
		if text == "" {
			return "", fmt.Errorf("the element with class %q is empty", class)
		}
		return text, nil
	default:
		return "", fmt.Errorf("%d elements with class %q, so which holds the value is ambiguous",
			len(matches), class)
	}
}

// currencyLabelIn finds the ISO-like code a rate box states about itself.
//
// The BCV writes it in a <span> beside the currency's flag image, with
// inconsistent whitespace: "<span> USD</span>" for one currency and
// "<span> EUR </span>" for the next. The first span in the box whose trimmed
// text is exactly three upper-case letters is the label; anything else is
// decoration.
//
// It returns "" when there is none, which the caller treats as a reason to fail
// rather than as a reason to assume.
func currencyLabelIn(root *html.Node) string {
	var label string
	walk(root, func(node *html.Node) bool {
		if node.Type != html.ElementNode || node.Data != "span" {
			return true
		}
		candidate := strings.TrimSpace(textOf(node))
		if len(candidate) != codeLength {
			return true
		}
		for _, r := range candidate {
			if r < 'A' || r > 'Z' {
				return true
			}
		}
		label = candidate
		return false
	})
	return label
}

// nodesContainingText returns every text node whose content contains needle.
//
// The caller uses the COUNT as well as the result: a label that appears twice
// makes the document ambiguous, and an ambiguous document is refused rather than
// resolved by taking the first match.
func nodesContainingText(root *html.Node, needle string) []*html.Node {
	var found []*html.Node
	walk(root, func(node *html.Node) bool {
		if node.Type == html.TextNode && strings.Contains(node.Data, needle) {
			found = append(found, node)
		}
		return true
	})
	return found
}

// walk visits every node depth-first. The callback returns false to stop the
// traversal entirely.
func walk(node *html.Node, visit func(*html.Node) bool) {
	var stopped bool
	var recurse func(*html.Node)
	recurse = func(n *html.Node) {
		if n == nil || stopped {
			return
		}
		if !visit(n) {
			stopped = true
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			recurse(child)
		}
	}
	recurse(node)
}

// textOf concatenates the text of a subtree.
func textOf(node *html.Node) string {
	var builder strings.Builder
	walk(node, func(n *html.Node) bool {
		if n.Type == html.TextNode {
			builder.WriteString(n.Data)
		}
		return true
	})
	return builder.String()
}

// attr returns an attribute's value, or "".
func attr(node *html.Node, name string) string {
	for _, a := range node.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// hasClass reports whether the class attribute contains the class as a whole
// word. Substring matching would make "strong-tb" match "strong-tbx".
func hasClass(node *html.Node, class string) bool {
	for _, candidate := range strings.Fields(attr(node, "class")) {
		if candidate == class {
			return true
		}
	}
	return false
}
