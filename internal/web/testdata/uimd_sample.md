# Heading one

## Heading *two* with `code`

A paragraph with **bold**, *italic*, _also italic_, ~~strike~~, `inline code`, ``double `tick` code``,
a second line, a [link](https://example.com/a_(b) "Title"), a [relative](/ui/x/shell/docs/a),
an <https://example.org/auto> autolink, a bare https://example.com/path?x=1&y=2, and
a bare one in brackets (https://example.com/wiki/A_(b)). Sentence end https://example.com/x.
Mentions @robin and @claude:mainex, refs REQ-5, PAIN-12, TASK-3, IDEA-7, DEC-2, FEAT-9
and a knowledge entry #1970. Escapes: \*not italic\*, \[not a link\], snake_case_word, 2*3*4.
A [bad](javascript:alert(1)) link and a [nested **bold [x](https://a.example)** text](https://b.example).
Unclosed *star and **double and _under and [bracket and `tick.

> a quote with **bold**
> second line
> > nested quote

- item one
- item two with *italic*
  - nested a
  - nested b
- [ ] open task
- [x] done task

1. first
2. second
3. third

7) seven
8) eight

| Name | Value | Right |
| :--- | :---: | ---: |
| a \| b | `c` | **d** |
| e | f | g |

```go
fmt.Println("<b>not html</b>")
```

````
```inside```
````

---

Last paragraph with *emphasis **strong inside** emphasis* and **bold *italic inside* bold**.
