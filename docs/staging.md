# Staging

Staging envs are optional remote envs attached to an existing site.

## Check Status

```sh
nf site staging status client.linode1
```

If a staging env exists, status prints the env ID and URL. If not, it prints the command to create one.

## Add Staging

Preview first:

```sh
nf site staging add client.linode1 --dry-run
```

Execute explicitly:

```sh
nf site staging add client.linode1 --execute --yes
```

You can also create staging while adding the site:

```sh
nf site add linode1 client --with-staging --execute --yes
```

## Remove Staging

Preview first:

```sh
nf site staging remove client.linode1 --dry-run
```

Execute explicitly:

```sh
nf site staging remove client.linode1 --execute --yes
```

`rm` is a shorthand for `remove`:

```sh
nf site staging rm client.linode1 --dry-run
```

`nf site remove client.linode1:staging` is intentionally rejected. Use `nf site staging remove <site>` to delete staging, or `nf site remove <site>` to delete the whole site.
