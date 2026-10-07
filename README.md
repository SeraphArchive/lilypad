# lilypad

A private server for Heaven Burns Red, written in Go.

> ⚠️ Warning
>
> This project is in active development and experimental. You may encounter bugs, incomplete features, or data loss.
> Always back up your data and use at your own risk. Please report issues if you find any.

## Usage

1. Download your platform's ZIP from [Releases](https://github.com/SeraphArchive/lilypad/releases) and extract it.
2. Provide a PostgreSQL database. Copy `config.example.yaml` to `config.local.yaml`
   and set `db.dsn`. Optionally set `data_dir` to your decoded master-data directory.
3. Run `./lilypad -config config.local.yaml` (`.\lilypad.exe` on Windows).
4. Open `http://localhost:8443` for the portal; check `/readyz` for readiness.
5. Create an account and get takeover code and password. Use them to log in from the game client.

Game connections require a configured [clientpatch](https://github.com/SeraphArchive/clientpatch).
Master data is not provided with this project and you need to manually fetch from the game's assets. Economy operations require master data.

## Building

With Git, Go (version in `go.mod`) and Python 3.11+:

```sh
python3 build.py
```

ZIPs and SHA-256 checksums appear under `dist/releases/`.
See [docs](./docs) for architecture, development and release maintenance.

## License

[MIT](LICENSE). Dependencies retain their own licenses.
