package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// kind is one file type: how to write a whole file of about n lines, and how
// to write one more block for a later version.
type kind struct {
	ext    string
	binary bool
	gen    func(r *rng, n int, b *bytes.Buffer)
	chunk  func(r *rng, b *bytes.Buffer)
}

var (
	kinds  []kind
	kindOf = map[string]int{}
)

// codeLang describes a language by templates; see rng.expand for the
// placeholders. Nothing here has to compile, it has to look like the language
// to a highlighter and a diff.
type codeLang struct {
	ext, cm, cmEnd string
	head, fn       string
	body           []string
	end, tail      string
}

func (l *codeLang) chunk(r *rng, b *bytes.Buffer) {
	if l.cm != "" {
		b.WriteString(l.cm + " " + r.str() + l.cmEnd + "\n")
	}
	b.WriteString(r.expand(l.fn) + "\n")
	for i, k := 0, 1+r.n(7); i < k; i++ {
		b.WriteString(r.expand(r.pick(l.body)) + "\n")
	}
	if l.end != "" {
		b.WriteString(r.expand(l.end) + "\n")
	}
	b.WriteByte('\n')
}

func (l *codeLang) gen(r *rng, n int, b *bytes.Buffer) {
	start := b.Len()
	b.WriteString(r.expand(l.head) + "\n")
	for bytes.Count(b.Bytes()[start:], []byte{'\n'}) < n {
		l.chunk(r, b)
	}
	b.WriteString(l.tail)
}

var langs = []codeLang{
	{ext: "go", cm: "//",
		head: "package $w\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n",
		fn:   "func $N($a int, $a string) (int, error) {",
		body: []string{"\t$a := $a + $n", "\tif $a > $n {\n\t\treturn $n, errors.New(\"$s\")\n\t}", "\tfmt.Println(\"$s\", $a)", "\tfor i := 0; i < $n; i++ {\n\t\t$a += i * $n\n\t}", "\t$a = append($a, $N{$a: $n})"},
		end:  "\treturn $n, nil\n}"},
	{ext: "c", cm: "/*", cmEnd: " */",
		head: "#include <stdint.h>\n#include <stdio.h>\n#include \"$u.h\"\n",
		fn:   "static int $u(struct $u *$w, uint32_t $u)\n{",
		body: []string{"\tint $u = $u + $n;", "\tif ($u > $n) {\n\t\treturn -$n;\n\t}", "\tprintf(\"$s %d\\n\", $u);", "\tfor (size_t i = 0; i < $n; i++) {\n\t\t$u[i] ^= 0x$n;\n\t}", "\t$w->$u = $u($w, $n);"},
		end:  "\treturn 0;\n}"},
	{ext: "h", cm: "/*", cmEnd: " */",
		head: "#pragma once\n\n#include <stddef.h>\n#include <stdint.h>\n",
		fn:   "struct $u {",
		body: []string{"\tuint32_t $u;", "\tconst char *$u; /* $s */", "\tsize_t $u[$n];"},
		end:  "};\n\nint $u(struct $u *$w, uint32_t $u);"},
	{ext: "cc", cm: "//",
		head: "#include <memory>\n#include <string>\n#include <vector>\n\nnamespace $w {\n",
		fn:   "std::unique_ptr<$N> $N::$a(const std::string& $a, int $a) {",
		body: []string{"  auto $a = std::make_unique<$N>($n);", "  if ($a > $n) {\n    throw std::runtime_error(\"$s\");\n  }", "  for (const auto& $a : $a_) {\n    $a.push_back($a * $n);\n  }", "  $a_->$a(std::move($a));"},
		end:  "  return $a;\n}", tail: "}  // namespace\n"},
	{ext: "rs", cm: "//",
		head: "use std::collections::HashMap;\nuse std::fmt;\n",
		fn:   "pub fn $u($u: &str, $u: usize) -> Result<usize, Error> {",
		body: []string{"    let $u = $u.len() + $n;", "    if $u > $n {\n        return Err(Error::new(\"$s\"));\n    }", "    let mut $u: HashMap<String, u64> = HashMap::new();", "    for ($u, $u) in $u.iter().enumerate() {\n        $u.insert($u.to_string(), $n);\n    }", "    println!(\"$s {}\", $u);"},
		end:  "    Ok($n)\n}"},
	{ext: "py", cm: "#",
		head: "import os\nimport sys\nfrom dataclasses import dataclass\n",
		fn:   "def $u($u, $u=$n):",
		body: []string{"    $u = $u + $n", "    if $u > $n:\n        raise ValueError(\"$s\")", "    for $w in range($n):\n        $u.append($w * $n)", "    print(f\"$s {$u}\")", "    $u = {\"$w\": $n, \"$w\": \"$s\"}"},
		end:  "    return $u\n"},
	{ext: "js", cm: "//",
		head: "'use strict';\n\nconst $a = require('./$w');\n",
		fn:   "async function $a($a, $a = $n) {",
		body: []string{"  const $a = $a + $n;", "  if ($a > $n) {\n    throw new Error('$s');\n  }", "  for (const $a of $a) {\n    $a.push($a * $n);\n  }", "  console.log(`$s ${$a}`);", "  const { $a, $a } = await $a($n);"},
		end:  "  return $a;\n}\n\nmodule.exports.$a = $a;"},
	{ext: "ts", cm: "//",
		head: "import { $N } from './$w';\nimport type { $N } from '../types';\n",
		fn:   "export async function $a($a: string, $a: number = $n): Promise<$N> {",
		body: []string{"  const $a: number = $a.length + $n;", "  if ($a > $n) {\n    throw new Error('$s');\n  }", "  const $a = $a.map(($a) => $a * $n).filter(Boolean);", "  console.log(`$s ${$a}`);", "  const $a: Record<string, $N> = {};"},
		end:  "  return { $a, $a };\n}"},
	{ext: "tsx", cm: "//",
		head: "import React, { useState } from 'react';\n",
		fn:   "export function $N({ $a, $a }: $NProps) {",
		body: []string{"  const [$a, set$N] = useState($n);", "  const $a = $a.map(($a) => <li key={$a.id}>{$a.$a}</li>);", "  if (!$a) {\n    return <p>$s</p>;\n  }"},
		end:  "  return (\n    <div className=\"$w-$w\">\n      <h2>$s</h2>\n      {$a}\n    </div>\n  );\n}"},
	{ext: "java", cm: "//",
		head: "package com.example.$w.$w;\n\nimport java.util.ArrayList;\nimport java.util.List;\n\npublic class $N {\n",
		fn:   "    public int $a(String $a, int $a) throws $NException {",
		body: []string{"        int $a = $a + $n;", "        if ($a > $n) {\n            throw new $NException(\"$s\");\n        }", "        List<$N> $a = new ArrayList<>();", "        for (int i = 0; i < $n; i++) {\n            $a.add(new $N(i * $n));\n        }", "        System.out.println(\"$s \" + $a);"},
		end:  "        return $a;\n    }", tail: "}\n"},
	{ext: "kt", cm: "//",
		head: "package com.example.$w\n",
		fn:   "fun $a($a: String, $a: Int = $n): Result<$N> {",
		body: []string{"    val $a = $a.length + $n", "    require($a < $n) { \"$s\" }", "    val $a = $a.map { it * $n }.filter { it > $n }", "    println(\"$s $$$a\")"},
		end:  "    return Result.success($N($a))\n}"},
	{ext: "swift", cm: "//",
		head: "import Foundation\n",
		fn:   "func $a(_ $a: String, $a: Int = $n) throws -> $N {",
		body: []string{"    let $a = $a.count + $n", "    guard $a < $n else {\n        throw $NError.$a(\"$s\")\n    }", "    var $a: [Int] = []", "    for $a in 0..<$n {\n        $a.append($a * $n)\n    }", "    print(\"$s \\($a)\")"},
		end:  "    return $N($a: $a)\n}"},
	{ext: "rb", cm: "#",
		head: "# frozen_string_literal: true\n\nrequire '$u'\n",
		fn:   "def $u($u, $u = $n)",
		body: []string{"  $u = $u + $n", "  raise ArgumentError, '$s' if $u > $n", "  $u.each_with_index do |$w, i|\n    $u << $w * $n\n  end", "  puts \"$s #{$u}\""},
		end:  "  $u\nend"},
	{ext: "php", cm: "//",
		head: "<?php\n\ndeclare(strict_types=1);\n\nnamespace App\\$N;\n",
		fn:   "function $u(string $$$u, int $$$u = $n): array\n{",
		body: []string{"    $$$u = $$$u + $n;", "    if ($$$u > $n) {\n        throw new \\RuntimeException('$s');\n    }", "    foreach ($$$u as $$$u => $$$u) {\n        $$$u[] = $$$u * $n;\n    }", "    echo \"$s {$$$u}\\n\";"},
		end:  "    return [$$$u, $n];\n}"},
	{ext: "sh", cm: "#",
		head: "#!/usr/bin/env bash\nset -euo pipefail\n",
		fn:   "$u() {",
		body: []string{"  local $u=\"$${1:-$n}\"", "  if [[ \"$$$u\" -gt $n ]]; then\n    echo \"$s\" >&2\n    return 1\n  fi", "  for $w in $$(seq 1 $n); do\n    $u+=(\"$$$w\")\n  done", "  echo \"$s $${$u}\""},
		end:  "}"},
	{ext: "cs", cm: "//",
		head: "using System;\nusing System.Collections.Generic;\n\nnamespace $N.$N;\n\npublic sealed class $N\n{",
		fn:   "    public int $N(string $a, int $a)\n    {",
		body: []string{"        var $a = $a.Length + $n;", "        if ($a > $n)\n        {\n            throw new InvalidOperationException(\"$s\");\n        }", "        var $a = new List<int>();", "        Console.WriteLine($$\"$s {$a}\");"},
		end:  "        return $a;\n    }", tail: "}\n"},
	{ext: "lua", cm: "--",
		head: "local $u = require(\"$w.$w\")\nlocal M = {}\n",
		fn:   "function M.$u($u, $u)",
		body: []string{"  local $u = $u + $n", "  if $u > $n then\n    error(\"$s\")\n  end", "  for i = 1, $n do\n    $u[i] = i * $n\n  end", "  print(\"$s\", $u)"},
		end:  "  return $u\nend", tail: "return M\n"},
	{ext: "proto", cm: "//",
		head: "syntax = \"proto3\";\n\npackage $w.$w.v1;\n",
		fn:   "message $N {",
		body: []string{"  string $u = $n;", "  repeated int64 $u = $n;", "  $N $u = $n; // $s", "  map<string, $N> $u = $n;"},
		end:  "}"},
	{ext: "sql", cm: "--",
		head: "-- $s\nBEGIN;\n",
		fn:   "CREATE TABLE IF NOT EXISTS $u (",
		body: []string{"  $u BIGINT NOT NULL DEFAULT $n,", "  $u TEXT, -- $s", "  $u TIMESTAMPTZ NOT NULL DEFAULT now(),"},
		end:  "  id BIGSERIAL PRIMARY KEY\n);\nINSERT INTO $u ($u, $u) VALUES ($n, '$s');", tail: "COMMIT;\n"},
	{ext: "css", cm: "/*", cmEnd: " */",
		head: "@charset \"utf-8\";\n",
		fn:   ".$w-$w > .$w:hover {",
		body: []string{"  margin: $npx $npx;", "  color: #$n;", "  font-family: \"$s\", sans-serif;", "  content: \"$s\";", "  grid-template-columns: repeat($n, minmax($npx, 1fr));"},
		end:  "}"},
	{ext: "html", cm: "<!--", cmEnd: " -->",
		head: "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>$s</title>\n</head>\n<body>",
		fn:   "<section id=\"$w-$n\">",
		body: []string{"  <h2>$s</h2>", "  <p>$s $s $s</p>", "  <a href=\"/$w/$w?$w=$n\">$s</a>", "  <img src=\"$w.png\" alt=\"$s\" width=\"$n\">"},
		end:  "</section>", tail: "</body>\n</html>\n"},
	{ext: "xml", cm: "<!--", cmEnd: " -->",
		head: "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<root>",
		fn:   "  <item id=\"$n\" name=\"$s\">",
		body: []string{"    <value key=\"$w\">$s</value>", "    <flag enabled=\"true\" weight=\"$n\"/>"},
		end:  "  </item>", tail: "</root>\n"},
	{ext: "yaml", cm: "#",
		head: "# $s\nversion: $n\n",
		fn:   "$u:",
		body: []string{"  $u: $n", "  $u: \"$s\"", "  $u:\n    - $w\n    - $w", "  $u: {enabled: true, retries: $n}"}},
	{ext: "toml", cm: "#",
		head: "# $s\n",
		fn:   "[$w.$w]",
		body: []string{"$u = $n", "$u = \"$s\"", "$u = [\"$w\", \"$w\", \"$w\"]", "$u = true"}},
	{ext: "md",
		head: "# $s\n",
		fn:   "## $s\n",
		body: []string{"$w $w $w $s $w $w $w $w $s $w $w.", "$p. $p. $p.", "- $s\n- $w $w $w\n- `$u($n)`", "> $s", "| $w | $w |\n|---|---|\n| $s | $n |", "```sh\n$w --$w=$n $w\n```", "$s. $s. [$w](https://example.invalid/$w/$n)"}},
	{ext: "txt",
		head: "$s\n",
		fn:   "$s",
		body: []string{"$w $w $w $w $w $w $w $w $w $w", "$p $p $p", "$s $s", "  $n  $w  $s"}},
	{ext: "csv",
		head: "id,name,kind,count,label",
		fn:   "$n,$w,$w,$n,$s",
		body: []string{"$n,$w,$w,$n,$s", "$n,$u,$w,$n,\"$s, $w\""}},
}

func init() {
	for i := range langs {
		l := &langs[i]
		kindOf[l.ext] = len(kinds)
		kinds = append(kinds, kind{ext: l.ext, gen: l.gen, chunk: l.chunk})
	}
	kindOf["json"] = len(kinds)
	kinds = append(kinds, kind{ext: "json", gen: genJSON, chunk: func(r *rng, b *bytes.Buffer) {
		fmt.Fprintf(b, "  \"%s_%d\": \"%s\",\n", r.snake(), r.n(1<<30), r.str())
	}})
	kindOf["png"] = len(kinds)
	kinds = append(kinds, kind{ext: "png", binary: true, gen: genPNG})
}

func genJSON(r *rng, n int, b *bytes.Buffer) {
	b.WriteString("{\n")
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("%s_%d", r.snake(), i)
		switch r.n(4) {
		case 0:
			fmt.Fprintf(b, "  \"%s\": %d,\n", key, r.n(1000000))
		case 1:
			fmt.Fprintf(b, "  \"%s\": \"%s\",\n", key, r.str())
		case 2:
			fmt.Fprintf(b, "  \"%s\": [\"%s\", \"%s\", %d],\n", key, r.pick(words), r.pick(words), r.n(99))
		default:
			fmt.Fprintf(b, "  \"%s\": {\"enabled\": true, \"label\": \"%s\"},\n", key, r.str())
		}
	}
	b.WriteString("  \"end\": null\n}\n")
}

// genPNG draws a small gradient with a few rectangles, so every image is a
// valid, distinct PNG.
func genPNG(r *rng, _ int, b *bytes.Buffer) {
	sizes := []int{16, 16, 24, 32, 32, 48, 64, 128}
	w := sizes[r.n(len(sizes))]
	img := image.NewNRGBA(image.Rect(0, 0, w, w))
	c1 := [3]int{r.n(256), r.n(256), r.n(256)}
	c2 := [3]int{r.n(256), r.n(256), r.n(256)}
	for y := 0; y < w; y++ {
		for x := 0; x < w; x++ {
			t := (x + y) * 255 / (2 * w)
			img.SetNRGBA(x, y, color.NRGBA{
				uint8((c1[0]*(255-t) + c2[0]*t) / 255),
				uint8((c1[1]*(255-t) + c2[1]*t) / 255),
				uint8((c1[2]*(255-t) + c2[2]*t) / 255), 255})
		}
	}
	for i := 0; i < 3; i++ {
		x0, y0, s := r.n(w), r.n(w), 2+r.n(w/2)
		c := color.NRGBA{uint8(r.n(256)), uint8(r.n(256)), uint8(r.n(256)), uint8(128 + r.n(128))}
		for y := y0; y < min(w, y0+s); y++ {
			for x := x0; x < min(w, x0+s); x++ {
				img.SetNRGBA(x, y, c)
			}
		}
	}
	if err := png.Encode(b, img); err != nil {
		panic(err)
	}
}
