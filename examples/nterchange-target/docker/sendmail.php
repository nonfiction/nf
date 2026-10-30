<?php
// PHP mail()/PEAR keep their MIME payload; msmtp handles SMTP and TLS.
$sender = getenv('NF_MAIL_FROM');
$name = getenv('NF_MAIL_NAME');
if (!$sender || !filter_var($sender, FILTER_VALIDATE_EMAIL) || !$name || strpbrk($name, "\r\n") !== false) {
    fwrite(STDERR, "Transactional mail sender configuration is invalid.\n");
    exit(78);
}
$message = stream_get_contents(STDIN);
$parts = preg_split('/\r?\n\r?\n/', $message, 2);
if (count($parts) !== 2) {
    fwrite(STDERR, "Transactional mail requires headers and a body.\n");
    exit(65);
}
$headers = [];
$originalFrom = null;
$hasReplyTo = false;
foreach (preg_split('/\r?\n(?![ \t])/', $parts[0]) as $header) {
    if (preg_match('/^From:\s*(.*)$/is', $header, $match)) {
        if ($originalFrom !== null) {
            fwrite(STDERR, "Transactional mail has duplicate From headers.\n");
            exit(65);
        }
        $originalFrom = trim(preg_replace('/\r?\n[ \t]+/', ' ', $match[1]));
        continue;
    }
    if (preg_match('/^(Return-Path|X-SMTPAPI):/i', $header)) {
        continue;
    }
    $hasReplyTo = $hasReplyTo || preg_match('/^Reply-To:/i', $header);
    $headers[] = $header;
}
// Contact forms may supply a visitor's From. Keep replies useful without spoofing that domain.
if ($originalFrom && !$hasReplyTo) {
    $headers[] = 'Reply-To: ' . $originalFrom;
}
$headers[] = 'From: "' . addcslashes($name, '\\"') . '" <' . $sender . '>';
// Password-reset and other transactional links must not become tracking links.
$headers[] = 'X-SMTPAPI: {"filters":{"clicktrack":{"settings":{"enable":0}},"opentrack":{"settings":{"enable":0}}}}';
$payload = implode("\r\n", $headers) . "\r\n\r\n" . $parts[1];
$process = proc_open(['/usr/bin/msmtp', '-t'], [0 => ['pipe', 'r'], 1 => STDOUT, 2 => STDERR], $pipes);
if (!is_resource($process)) {
    exit(69);
}
for ($offset = 0; $offset < strlen($payload); $offset += $written) {
    $written = fwrite($pipes[0], substr($payload, $offset, 65536));
    if ($written === false || $written === 0) {
        fclose($pipes[0]);
        proc_terminate($process);
        proc_close($process);
        exit(74);
    }
}
fclose($pipes[0]);
exit(proc_close($process));
