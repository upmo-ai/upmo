# Third-party notices

This repository uses work from other projects. This file names each one, says where it is used and gives its licence. The Upmo code and documentation are under the licence in [LICENSE](LICENSE). The licences below cover only the third-party files named here.

More entries are added as assets and libraries are bundled.

## Fonts

### Manrope

- Project: [Manrope](https://fonts.google.com/specimen/Manrope) by Mikhail Sharanda
- Licence: [SIL Open Font License 1.1](https://openfontlicense.org)
- Used in: the web build, which bundles the font as the UI typeface.

## Code

### Monaco Editor

- Project: [Monaco Editor](https://github.com/microsoft/monaco-editor) by Microsoft Corporation
- Licence: MIT
- Used in: the web build (`monaco-editor` 0.57.0), which bundles the editor, its worker, its icon font and its language tokenizers to show code, text files and diffs read only in the Artifacts tab. It loads lazily from the app's own files, never from a CDN.
- The package carries further notices for the code it incorporates (the `marked` Markdown parser, MIT, and language grammars adapted from other projects) in its `ThirdPartyNotices.txt`. It depends on [marked](https://github.com/markedjs/marked) 14.0.0 (MIT) and [DOMPurify](https://github.com/cure53/DOMPurify) 3.4.15 (MPL-2.0 or Apache-2.0).

## Password lists

### SecLists common credentials

- Project: [SecLists](https://github.com/danielmiessler/SecLists) by Daniel Miessler and contributors
- Licence: MIT
- Used in: `internal/identity/domain/common_passwords.go`, a filtered snapshot of `xato-net-10-million-passwords-1000000.txt` and `100k-most-used-passwords-NCSC.txt`. Only entries of at least 15 characters are kept. The NCSC list is redistributed inside SecLists. The raw lists are not in this repository.
