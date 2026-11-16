package docx

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// elem is a minimal XML element tree.
//
// Why not encoding/xml struct unmarshalling? WordprocessingML is huge and
// we only read a small, scattered part of it. Declaring Go structs for every
// path we touch would be long and brittle, while walking a generic tree lets
// the importer read like the spec: p.child("w:pPr").child("w:jc").attr("w:val").
//
// Names are stored with their conventional prefix ("w:p", "r:id") mapped
// from the namespace URI, not the prefix the file happened to use, so the
// code stays readable and still matches correctly if a producer picks
// different prefixes.
type elem struct {
	name string
	attr map[string]string
	kids []*elem
	text string // character data directly inside this element
}

// prefixes maps the namespace URIs we care about to their usual prefixes.
var prefixes = map[string]string{
	"http://schemas.openxmlformats.org/wordprocessingml/2006/main":            "w",
	"http://schemas.openxmlformats.org/officeDocument/2006/relationships":     "r",
	"http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing":  "wp",
	"http://schemas.openxmlformats.org/drawingml/2006/main":                   "a",
	"http://schemas.openxmlformats.org/drawingml/2006/picture":                "pic",
	"http://schemas.openxmlformats.org/package/2006/relationships":            "rel",
	"http://schemas.openxmlformats.org/markup-compatibility/2006":             "mc",
	"http://schemas.microsoft.com/office/word/2010/wordprocessingShape":       "wps",
	"http://schemas.openxmlformats.org/package/2006/metadata/core-properties": "cp",
	"http://purl.org/dc/elements/1.1/":                                        "dc",
}

func qname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	if p, ok := prefixes[n.Space]; ok {
		return p + ":" + n.Local
	}
	// Unknown namespace: keep it distinguishable from known ones.
	return "{" + n.Space + "}" + n.Local
}

// parseXML reads a whole document into an elem tree and returns its root.
func parseXML(data []byte) (*elem, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	root := &elem{}
	stack := []*elem{root}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		top := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			e := &elem{name: qname(t.Name), attr: make(map[string]string, len(t.Attr))}
			for _, a := range t.Attr {
				e.attr[qname(a.Name)] = a.Value
			}
			top.kids = append(top.kids, e)
			stack = append(stack, e)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			top.text += string(t)
		}
	}
	if len(root.kids) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return root.kids[0], nil
}

// The accessors below are nil-safe so lookups can be chained without
// checking every step: a missing element anywhere just yields nil / "".

func (e *elem) child(name string) *elem {
	if e == nil {
		return nil
	}
	for _, k := range e.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

func (e *elem) children(name string) []*elem {
	if e == nil {
		return nil
	}
	var out []*elem
	for _, k := range e.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

func (e *elem) attrv(name string) string {
	if e == nil {
		return ""
	}
	return e.attr[name]
}

// val returns the w:val attribute of the named child element.
func (e *elem) val(childName string) string {
	return e.child(childName).attrv("w:val")
}

// find returns the first descendant (depth first) with the given name.
func (e *elem) find(name string) *elem {
	if e == nil {
		return nil
	}
	for _, k := range e.kids {
		if k.name == name {
			return k
		}
		if f := k.find(name); f != nil {
			return f
		}
	}
	return nil
}

// toggle reads an OOXML "on/off" property such as <w:b/>. A bare element
// means on; w:val of 0/false/off means explicitly off. The second result
// reports whether the property was present at all, which matters because
// "explicitly off" must override a style that turns it on.
func (e *elem) toggle(childName string) (on, present bool) {
	c := e.child(childName)
	if c == nil {
		return false, false
	}
	switch strings.ToLower(c.attrv("w:val")) {
	case "0", "false", "off", "none":
		return false, true
	}
	return true, true
}
