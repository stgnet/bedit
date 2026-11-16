// Package docxtest builds small .docx files in memory for tests. The parts
// mimic what Google's export writes (same element shapes and attributes),
// cut down to the essentials, so tests don't need binary fixtures.
package docxtest

import (
	"archive/zip"
	"bytes"
)

// Build assembles a .docx whose document body is body.
func Build(body string) []byte {
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
		`xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" ` +
		`xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"`
	parts := map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?><w:document ` + ns + `><w:body>` + body +
			`<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:bottom="1440" w:left="1440" w:right="1440"/></w:sectPr></w:body></w:document>`,
		"word/styles.xml": `<?xml version="1.0"?><w:styles ` + ns + `>
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial"/><w:sz w:val="22"/></w:rPr></w:rPrDefault>
<w:pPrDefault><w:pPr><w:spacing w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:styleId="Normal" w:default="1"><w:name w:val="normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/>
<w:pPr><w:spacing w:before="400" w:after="120"/></w:pPr><w:rPr><w:b/><w:sz w:val="40"/></w:rPr></w:style>
</w:styles>`,
		"word/numbering.xml": `<?xml version="1.0"?><w:numbering ` + ns + `>
<w:abstractNum w:abstractNumId="1">
<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/><w:pPr><w:ind w:left="720" w:hanging="360"/></w:pPr></w:lvl>
<w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="lowerLetter"/><w:lvlText w:val="%2."/><w:pPr><w:ind w:left="1440" w:hanging="360"/></w:pPr></w:lvl>
</w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="1"/></w:num></w:numbering>`,
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/" TargetMode="External"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.png"/>
</Relationships>`,
		"word/media/image1.png": "\x89PNG fake image bytes",
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range parts {
		w, err := zw.Create(name)
		if err != nil {
			panic(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// sampleBody exercises most of what the importer handles: a heading with
// a bookmark and a run that turns off the style's bold, split runs to be
// merged, color and italics, external and internal links, a two-level
// numbered list, a wrapped and cropped image, and a table with a vertical
// merge and shading.
const sampleBody = `
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:bookmarkStart w:id="0" w:name="_h1"/><w:bookmarkEnd w:id="0"/>
  <w:r><w:t>Big </w:t></w:r><w:r><w:rPr><w:b w:val="0"/></w:rPr><w:t>plain</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">Hello </w:t></w:r><w:r><w:t xml:space="preserve">world, </w:t></w:r>
  <w:r><w:rPr><w:i/><w:color w:val="FF0000"/></w:rPr><w:t>red</w:t></w:r>
  <w:hyperlink r:id="rId1"><w:r><w:t>link</w:t></w:r></w:hyperlink>
  <w:hyperlink w:anchor="_h1"><w:r><w:t>back</w:t></w:r></w:hyperlink></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>one</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>one-a</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>two</w:t></w:r></w:p>
<w:p><w:r><w:t>Wrapped</w:t></w:r><w:r><w:drawing>
  <wp:anchor behindDoc="0" distT="114300" distB="114300" distL="114300" distR="114300">
  <wp:positionH relativeFrom="column"><wp:posOffset>3443288</wp:posOffset></wp:positionH>
  <wp:positionV relativeFrom="paragraph"><wp:posOffset>171450</wp:posOffset></wp:positionV>
  <wp:extent cx="2500313" cy="2299816"/><wp:wrapSquare wrapText="bothSides"/><wp:docPr id="1" name="x" descr="a photo"/>
  <a:graphic><a:graphicData><pic:pic><pic:blipFill><a:blip r:embed="rId2"/><a:srcRect l="10000" r="10000"/></pic:blipFill></pic:pic></a:graphicData></a:graphic>
  </wp:anchor></w:drawing></w:r></w:p>
<w:tbl><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="8" w:color="000000"/></w:tblBorders></w:tblPr>
  <w:tblGrid><w:gridCol w:w="2880"/><w:gridCol w:w="2880"/></w:tblGrid>
  <w:tr><w:tc><w:tcPr><w:vMerge w:val="restart"/><w:shd w:fill="FFFF00"/></w:tcPr><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc>
        <w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc></w:tr>
  <w:tr><w:tc><w:tcPr><w:vMerge/></w:tcPr><w:p/></w:tc><w:tc><w:p><w:r><w:t>C</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>`

// Sample returns the sample document.
func Sample() []byte { return Build(sampleBody) }
