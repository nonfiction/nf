package cli

func linodeNginxCacheShellFunctions() string {
	return `nf_linode_write_cache_snippets() {
  install -d -m 0755 /etc/nginx/snippets
  cat >/etc/nginx/snippets/nf-fastcgi-cache-bypass.conf <<'EOF'
set $nf_skip_cache 0;
if ($request_method !~ ^(GET|HEAD)$) { set $nf_skip_cache 1; }
if ($query_string != "") { set $nf_skip_cache 1; }
if ($http_authorization != "") { set $nf_skip_cache 1; }
if ($request_uri ~* "/wp-admin/|/wp-login\.php|/wp-cron\.php|/xmlrpc\.php|/wp-json/|/cart|/checkout|/my-account") { set $nf_skip_cache 1; }
if ($http_cookie ~* "comment_author|wordpress_[a-f0-9]+|wp-postpass|wordpress_logged_in|woocommerce_items_in_cart|woocommerce_cart_hash|wp_woocommerce_session") { set $nf_skip_cache 1; }
EOF
  cat >/etc/nginx/snippets/nf-fastcgi-cache.conf <<'EOF'
fastcgi_cache_methods GET HEAD;
fastcgi_cache_bypass $nf_skip_cache $http_authorization;
fastcgi_no_cache $nf_skip_cache $http_authorization $upstream_http_set_cookie;
fastcgi_cache_valid 200 301 302 10m;
fastcgi_cache_valid 404 1m;
fastcgi_cache_lock on;
fastcgi_cache_use_stale error timeout updating http_500 http_503;
add_header X-NF-Cache $upstream_cache_status always;
EOF
}
nf_linode_cache_slug() { basename "$(dirname "$1")"; }
nf_linode_cache_zone() { printf 'nf_cache_%s' "$(printf '%s' "$1" | sed 's/[^A-Za-z0-9_]/_/g')"; }
nf_linode_ensure_cache_config() {
  local site_path=$1 env_slug cache_zone cache_path cache_conf
  env_slug=$(nf_linode_cache_slug "$site_path")
  cache_zone=$(nf_linode_cache_zone "$env_slug")
  cache_path="/var/cache/nginx/nf/sites/$env_slug"
  cache_conf="/etc/nginx/conf.d/nf-cache-$env_slug.conf"
  install -d -o www-data -g www-data -m 2775 "$cache_path"
  cat >"$cache_conf" <<EOF
fastcgi_cache_path $cache_path levels=1:2 keys_zone=$cache_zone:20m inactive=60m max_size=512m use_temp_path=off;
EOF
  printf '%s\n' "$cache_zone"
}
nf_linode_install_cache_mu_plugin() {
  local site_path=$1 plugin_path
  install -d -o www-data -g www-data -m 2775 "$site_path/wp-content/mu-plugins"
  plugin_path="$site_path/wp-content/mu-plugins/nf-linode-cache.php"
  cat >"$plugin_path" <<'PHP'
<?php
/*
Plugin Name: nf Linode Cache
Description: Adds authenticated purge controls for nf-managed Linode page cache.
*/

if (!defined('ABSPATH')) {
    return;
}

function nf_linode_cache_dir() {
    $root = rtrim(ABSPATH, '/\\');
    if ($root === '') {
        return '';
    }
    $env = basename(dirname($root));
    if ($env === '' || $env === '.' || $env === '..') {
        return '';
    }
    return '/var/cache/nginx/nf/sites/' . $env;
}

function nf_linode_cache_can_purge() {
    return current_user_can('manage_options');
}

function nf_linode_cache_purge_dir($dir) {
    $base = '/var/cache/nginx/nf/sites';
    $realBase = realpath($base);
    if ($realBase === false) {
        return new WP_Error('nf_linode_cache_base_missing', 'The nf cache directory does not exist.');
    }
    $realDir = realpath($dir);
    if ($realDir === false) {
        return true;
    }
    if (strpos($realDir . DIRECTORY_SEPARATOR, $realBase . DIRECTORY_SEPARATOR) !== 0) {
        return new WP_Error('nf_linode_cache_unsafe_path', 'The nf cache path is outside the managed cache directory.');
    }
    $iterator = new RecursiveIteratorIterator(
        new RecursiveDirectoryIterator($realDir, FilesystemIterator::SKIP_DOTS),
        RecursiveIteratorIterator::CHILD_FIRST
    );
    foreach ($iterator as $item) {
        $path = $item->getPathname();
        if ($item->isDir() && !$item->isLink()) {
            @rmdir($path);
        } else {
            @unlink($path);
        }
    }
    return true;
}

function nf_linode_cache_purge() {
    $result = nf_linode_cache_purge_dir(nf_linode_cache_dir());
    if (is_wp_error($result)) {
        return $result;
    }
    wp_cache_flush();
    return true;
}

add_action('admin_post_nf_linode_cache_purge', function () {
    if (!nf_linode_cache_can_purge()) {
        wp_die(esc_html__('You are not allowed to purge the site cache.', 'nf'));
    }
    check_admin_referer('nf_linode_cache_purge');
    $result = nf_linode_cache_purge();
    $redirect = wp_get_referer() ?: admin_url();
    $arg = is_wp_error($result) ? 'nf_linode_cache_error' : 'nf_linode_cache_purged';
    wp_safe_redirect(add_query_arg($arg, '1', $redirect));
    exit;
});

function nf_linode_cache_purge_url() {
    return wp_nonce_url(admin_url('admin-post.php?action=nf_linode_cache_purge'), 'nf_linode_cache_purge');
}

add_action('admin_bar_menu', function ($bar) {
    if (!nf_linode_cache_can_purge()) {
        return;
    }
    $bar->add_node([
        'id' => 'nf-linode-cache-purge',
        'title' => 'Clear nf Cache',
        'href' => nf_linode_cache_purge_url(),
    ]);
}, 100);

add_action('wp_dashboard_setup', function () {
    if (!nf_linode_cache_can_purge()) {
        return;
    }
	wp_add_dashboard_widget('nf_linode_cache', 'nf Cache', function () {
		printf('<p>%s</p>', esc_html__('Clear this environment nginx page cache and WordPress object cache.', 'nf'));
		printf('<p><a class="button button-primary" href="%s">%s</a></p>', esc_url(nf_linode_cache_purge_url()), esc_html__('Clear Cache', 'nf'));
	});
});

add_action('admin_notices', function () {
    if (!nf_linode_cache_can_purge()) {
        return;
    }
    if (isset($_GET['nf_linode_cache_purged'])) {
        echo '<div class="notice notice-success is-dismissible"><p>' . esc_html__('nf cache cleared.', 'nf') . '</p></div>';
    }
    if (isset($_GET['nf_linode_cache_error'])) {
        echo '<div class="notice notice-error is-dismissible"><p>' . esc_html__('nf cache could not be cleared.', 'nf') . '</p></div>';
    }
});
PHP
  chown www-data:www-data "$plugin_path"
  chmod 0644 "$plugin_path"
}
`
}
