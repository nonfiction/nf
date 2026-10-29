FROM php:8.5.12-apache

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates libcurl4-openssl-dev libxml2-dev libmagickwand-dev \
      imagemagick msmtp libpng-dev libfreetype6-dev \
    && docker-php-ext-configure gd --with-freetype \
    && docker-php-ext-install -j"$(nproc)" mysqli curl simplexml gd \
    && pecl install apcu-5.1.28 imagick-3.8.1 \
    && docker-php-ext-enable apcu imagick \
    && a2enmod rewrite headers expires \
    && rm -rf /var/lib/apt/lists/*

COPY docker/apache.conf /etc/apache2/sites-available/000-default.conf
COPY docker/php.ini /usr/local/etc/php/conf.d/nterchange.ini
COPY docker/msmtprc /etc/msmtprc
WORKDIR /var/www/site
