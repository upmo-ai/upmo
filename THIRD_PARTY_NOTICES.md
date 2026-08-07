# Third-party notices

This repository uses work from other projects. This file names each one, says where it is used and gives its licence. The Upmo code and documentation are under the licence in [LICENSE](LICENSE). The licences below cover only the third-party files named here.

More entries are added as assets are bundled.

## Fonts

### Manrope

- Project: [Manrope](https://fonts.google.com/specimen/Manrope) by Mikhail Sharanda
- Licence: [SIL Open Font License 1.1](https://openfontlicense.org)
- Used in: the web build, which bundles the font as the UI typeface.

## Password lists

### SecLists common credentials

- Project: [SecLists](https://github.com/danielmiessler/SecLists) by Daniel Miessler and contributors
- Licence: MIT
- Used in: `internal/identity/domain/common_passwords.go`, a filtered snapshot of `xato-net-10-million-passwords-1000000.txt` and `100k-most-used-passwords-NCSC.txt`. Only entries of at least 15 characters are kept. The NCSC list is redistributed inside SecLists. The raw lists are not in this repository.
