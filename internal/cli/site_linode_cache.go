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
Plugin Name: nf Server Cache
Description: Adds authenticated controls for nf-managed Linode page and object cache.
*/

if (!defined('ABSPATH')) {
    return;
}

const NF_LINODE_CACHE_AUTOPURGE_OPTION = 'nf_linode_cache_autopurge';
const NF_LINODE_CACHE_NOTICE_QUERY = 'nf_linode_cache_notice';

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

function nf_linode_cache_can_manage() {
    return current_user_can('manage_options');
}

function nf_linode_cache_autopurge_enabled() {
    return get_option(NF_LINODE_CACHE_AUTOPURGE_OPTION, '1') !== '0';
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

function nf_linode_cache_clear_site() {
    $result = nf_linode_cache_purge_dir(nf_linode_cache_dir());
    if (is_wp_error($result)) {
        return $result;
    }
    return true;
}

function nf_linode_cache_clear_object() {
    wp_cache_flush();
    return true;
}

function nf_linode_cache_clear_all() {
    $result = nf_linode_cache_clear_site();
    if (is_wp_error($result)) {
        return $result;
    }
    return nf_linode_cache_clear_object();
}

function nf_linode_cache_redirect($notice) {
    $redirect = wp_get_referer() ?: admin_url('admin.php?page=server-tools');
    wp_safe_redirect(add_query_arg(NF_LINODE_CACHE_NOTICE_QUERY, rawurlencode($notice), $redirect));
    exit;
}

function nf_linode_cache_action_url($action) {
    return wp_nonce_url(admin_url('admin-post.php?action=nf_linode_cache_' . $action), 'nf_linode_cache_' . $action);
}

function nf_linode_cache_handle_clear($action, $callback, $success_notice) {
    if (!nf_linode_cache_can_manage()) {
        wp_die(esc_html__('You are not allowed to manage server cache.', 'nf'));
    }
    check_admin_referer('nf_linode_cache_' . $action);
    $result = call_user_func($callback);
    nf_linode_cache_redirect(is_wp_error($result) ? 'error' : $success_notice);
}

add_action('admin_post_nf_linode_cache_clear_all', function () {
    nf_linode_cache_handle_clear('clear_all', 'nf_linode_cache_clear_all', 'all_cleared');
});

add_action('admin_post_nf_linode_cache_clear_site', function () {
    nf_linode_cache_handle_clear('clear_site', 'nf_linode_cache_clear_site', 'site_cleared');
});

add_action('admin_post_nf_linode_cache_clear_object', function () {
    nf_linode_cache_handle_clear('clear_object', 'nf_linode_cache_clear_object', 'object_cleared');
});

add_action('admin_post_nf_linode_cache_save_settings', function () {
    if (!nf_linode_cache_can_manage()) {
        wp_die(esc_html__('You are not allowed to manage server cache.', 'nf'));
    }
    check_admin_referer('nf_linode_cache_save_settings');
    update_option(NF_LINODE_CACHE_AUTOPURGE_OPTION, isset($_POST['autopurge']) ? '1' : '0', false);
    nf_linode_cache_redirect('settings_saved');
});

add_action('admin_bar_menu', function ($bar) {
    if (!nf_linode_cache_can_manage()) {
        return;
    }
    $bar->add_node([
        'id' => 'nf-linode-cache-flush',
        'parent' => 'top-secondary',
        'title' => 'Flush',
        'href' => nf_linode_cache_action_url('clear_all'),
    ]);
    $bar->add_node([
        'id' => 'nf-linode-cache-clear-caches',
        'parent' => 'top-secondary',
        'title' => 'Clear Caches',
        'href' => nf_linode_cache_action_url('clear_all'),
    ]);
    $bar->add_node([
        'id' => 'nf-linode-cache-clear-all',
        'parent' => 'nf-linode-cache-clear-caches',
        'title' => 'Clear All Caches',
        'href' => nf_linode_cache_action_url('clear_all'),
    ]);
    $bar->add_node([
        'id' => 'nf-linode-cache-clear-site',
        'parent' => 'nf-linode-cache-clear-caches',
        'title' => 'Clear Site Cache',
        'href' => nf_linode_cache_action_url('clear_site'),
    ]);
    $bar->add_node([
        'id' => 'nf-linode-cache-clear-object',
        'parent' => 'nf-linode-cache-clear-caches',
        'title' => 'Clear Object Cache',
        'href' => nf_linode_cache_action_url('clear_object'),
    ]);
}, 100);

add_action('admin_menu', function () {
    if (!nf_linode_cache_can_manage()) {
        return;
    }
    add_menu_page('Server Cache', 'Server Cache', 'manage_options', 'server-tools', 'nf_linode_cache_render_page', 'dashicons-cloud', 58);
});

function nf_linode_cache_button($action, $label) {
    printf(
        '<p><a class="button button-primary" href="%s">%s</a></p>',
        esc_url(nf_linode_cache_action_url($action)),
        esc_html($label)
    );
}

function nf_linode_cache_render_page() {
    if (!nf_linode_cache_can_manage()) {
        return;
    }
    ?>
    <div class="wrap nf-server-cache-wrap">
        <h1><?php echo esc_html__('Server Cache', 'nf'); ?></h1>
        <div class="nf-server-cache-panel">
            <section class="nf-server-cache-row nf-server-cache-row-intro">
                <div>
                    <h2><?php echo esc_html__('Cache Control', 'nf'); ?></h2>
                    <p><?php echo esc_html__('This environment uses nginx page caching for anonymous traffic and the standard WordPress object cache. Clear caches when you need visitors to see recent changes immediately.', 'nf'); ?></p>
                </div>
                <div class="nf-server-cache-action"><?php nf_linode_cache_button('clear_all', __('Clear All Caches', 'nf')); ?></div>
            </section>
            <section class="nf-server-cache-row">
                <div>
                    <h2><?php echo esc_html__('Site Caching', 'nf'); ?></h2>
                    <p><?php echo esc_html__('Site cache stores full anonymous page responses on the server. Clearing it purges this environment cache directory.', 'nf'); ?></p>
                </div>
                <div class="nf-server-cache-action"><?php nf_linode_cache_button('clear_site', __('Clear Site Cache', 'nf')); ?></div>
            </section>
            <section class="nf-server-cache-row">
                <div>
                    <h2><?php echo esc_html__('Object Caching', 'nf'); ?></h2>
                    <p><?php echo esc_html__('The WordPress object cache stores runtime data used by plugins, themes, and core. Without a persistent backend such as Redis, this clears the active runtime cache.', 'nf'); ?></p>
                </div>
                <div class="nf-server-cache-action"><?php nf_linode_cache_button('clear_object', __('Clear Object Cache', 'nf')); ?></div>
            </section>
            <section class="nf-server-cache-row">
                <div>
                    <h2><?php echo esc_html__('Settings', 'nf'); ?></h2>
                    <form method="post" action="<?php echo esc_url(admin_url('admin-post.php')); ?>">
                        <input type="hidden" name="action" value="nf_linode_cache_save_settings">
                        <?php wp_nonce_field('nf_linode_cache_save_settings'); ?>
                        <label>
                            <input type="checkbox" name="autopurge" value="1" <?php checked(nf_linode_cache_autopurge_enabled()); ?>>
                            <?php echo esc_html__('Enable Autopurge', 'nf'); ?>
                        </label>
                        <p class="description"><?php echo esc_html__('Autopurge clears the full page cache when published content changes. Disable it temporarily during large imports if needed.', 'nf'); ?></p>
                        <p><button type="submit" class="button button-primary"><?php echo esc_html__('Save Settings', 'nf'); ?></button></p>
                    </form>
                </div>
            </section>
        </div>
    </div>
    <style>
        .nf-server-cache-wrap { max-width: 1180px; }
        .nf-server-cache-panel { background: #fff; margin-top: 18px; border: 1px solid #e5e7eb; }
        .nf-server-cache-row { display: grid; grid-template-columns: minmax(0, 1fr) 220px; gap: 32px; align-items: center; padding: 28px 32px; border-top: 1px solid #e5e7eb; }
        .nf-server-cache-row:first-child { border-top: 0; }
        .nf-server-cache-row h2 { margin: 0 0 18px; font-size: 20px; }
        .nf-server-cache-row p { max-width: 760px; }
        .nf-server-cache-action { text-align: right; }
        .nf-server-cache-action .button { min-width: 150px; text-align: center; }
        @media (max-width: 782px) { .nf-server-cache-row { grid-template-columns: 1fr; } .nf-server-cache-action { text-align: left; } }
    </style>
    <?php
}

function nf_linode_cache_autopurge() {
    if (!nf_linode_cache_autopurge_enabled()) {
        return;
    }
    if (get_transient('nf_linode_cache_autopurge_lock')) {
        return;
    }
    set_transient('nf_linode_cache_autopurge_lock', '1', 30);
    nf_linode_cache_clear_site();
}

add_action('transition_post_status', function ($newStatus, $oldStatus, $post) {
    if (!$post instanceof WP_Post) {
        return;
    }
    if ($newStatus !== 'publish' && $oldStatus !== 'publish') {
        return;
    }
    if (wp_is_post_revision($post->ID) || wp_is_post_autosave($post->ID)) {
        return;
    }
    nf_linode_cache_autopurge();
}, 10, 3);

add_action('deleted_post', 'nf_linode_cache_autopurge');
add_action('trashed_post', 'nf_linode_cache_autopurge');
add_action('switch_theme', 'nf_linode_cache_autopurge');

add_action('admin_notices', function () {
    if (!nf_linode_cache_can_manage()) {
        return;
    }
    $notice = isset($_GET[NF_LINODE_CACHE_NOTICE_QUERY]) ? sanitize_key(wp_unslash($_GET[NF_LINODE_CACHE_NOTICE_QUERY])) : '';
    $messages = [
        'all_cleared' => __('All caches cleared.', 'nf'),
        'site_cleared' => __('Site cache cleared.', 'nf'),
        'object_cleared' => __('Object cache cleared.', 'nf'),
        'settings_saved' => __('Server cache settings saved.', 'nf'),
    ];
    if (isset($messages[$notice])) {
        echo '<div class="notice notice-success is-dismissible"><p>' . esc_html($messages[$notice]) . '</p></div>';
    } elseif ($notice === 'error') {
        echo '<div class="notice notice-error is-dismissible"><p>' . esc_html__('Server cache could not be cleared.', 'nf') . '</p></div>';
    }
});
PHP
  chown www-data:www-data "$plugin_path"
  chmod 0644 "$plugin_path"
}
`
}
