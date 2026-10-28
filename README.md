# Apache Answer for Laravel Cloud

> [!IMPORTANT]
> This is an unofficial deployment wrapper. It is not an Apache Software Foundation project and is not endorsed by the ASF.

This repository packages the official Apache Answer release for installation through Scalpels on Laravel Cloud. Cloud supplies MySQL, Valkey, and public object storage. Scalpels supplies the owner's name, email, and public bucket URL. The wrapper writes those values into the database-backed Answer and plugin settings before the server starts.

## What the wrapper configures

Every prepare and start follows the same sequence.

1. writes Answer's MySQL and server configuration;
2. initializes or upgrades the database;
3. configures and enables the `redis_cache` and `s3_storage` plugins; and
4. allows `txt`, `pdf`, and `zip` attachments in Answer's advanced settings.

The Redis plugin receives the private Cloud URL with its TLS scheme, port, and database index intact. Credentials are stored in the plugin's separate username and password fields. The S3 plugin uses Cloud's physical bucket ID, endpoint, credentials, region `auto`, object prefix `answer/`, and the bucket's public URL. It does not set a public-read object ACL.

## Owner password

The initial owner is created from the installing Scalpels account. The account name is adapted to Answer's 2–30 character username rules while the email is preserved. Its bootstrap password is generated inside the wrapper and is not retained or printed.

Scalpels reveals a usable password by running the fixed command below inside the installed application:

```sh
./app owner-password
```

The command generates a new password, replaces the owner's current password, and prints one line:

```text
SCALPELS_OWNER_PASSWORD=<new-password>
```

Running the command again rotates the password again. It does not accept a password through an argument or environment variable.

## Mail

Laravel Cloud's managed mail attachment does not inject SMTP credentials into this Go application. Configure SMTP in Answer's administration area after installation. Once mail works, use **Forgot password** with the email you use in Scalpels.

## Build provenance

`build.sh` downloads Apache Answer 2.0.3 from the official GitHub release and verifies its SHA-256 checksum. It checks out the Apache Answer Plugins repository at commit `a575b18c298a4e6b57875d2805d3b614fad980d3`, applies [`plugins.patch`](plugins.patch), and builds the Redis cache and S3 storage plugins into the Answer binary.

The patch changes only the two bundled plugins. It adds `rediss://` support to the Redis plugin while keeping certificate verification enabled, and corrects attachment MIME types in the S3 plugin.

## License

The wrapper, Apache Answer, and Apache Answer Plugins are distributed under the Apache License 2.0. See [`LICENSE`](LICENSE), [`NOTICE`](NOTICE), and the bundled third-party license texts in [`licenses/`](licenses/).
