FROM phusion/baseimage:0.9.19

#ENV MYSQL_URL mysql://
ENV ENVIRONMENT DEV
ENV LOG_LEVEL INFO
ENV LANG en_US.UTF-8
ENV LC_ALL en_US.UTF-8
ENV APP_ROOT /app
ENV WSGI_APP ${APP_ROOT}/hive/server/serve.py
ENV HTTP_SERVER_PORT 8080

RUN \
    apt-get update && \
    apt-get install -y \
        build-essential \
        daemontools \
        git \
        libffi-dev \
        libmysqlclient-dev \
        libssl-dev \
        make \
        python3 \
        python3-dev \
        python3-pip \
        libxml2-dev \
        libxslt-dev \
        runit \
        nginx


RUN \
  mkdir -p /var/lib/nginx/body && \
  mkdir -p /var/lib/nginx/scgi && \
  mkdir -p /var/lib/nginx/uwsgi && \
  mkdir -p /var/lib/nginx/fastcgi && \
  mkdir -p /var/lib/nginx/proxy && \
  chown -R www-data:www-data /var/lib/nginx && \
  mkdir -p /var/log/nginx && \
  touch /var/log/nginx/access.log && \
  touch /var/log/nginx/error.log && \
  chown www-data:www-data /var/log/nginx/*.log && \
  touch /var/run/nginx.pid && \
  chown www-data:www-data /var/run/nginx.pid

ADD . /app

RUN \
    mv /app/service/* /etc/service && \
    chmod +x /etc/service/*/run

WORKDIR /app

RUN \
    pip3 install --upgrade pip && \
    pip3 install pipenv && \
	  pipenv lock && \
	  pipenv install --three --dev && \
    apt-get remove -y \
        build-essential \
        libffi-dev \
        libssl-dev && \
    apt-get autoremove -y && \
    rm -rf \
        /root/.cache \
        /var/lib/apt/lists/* \
        /tmp/* \
        /var/tmp/* \
        /var/cache/* \
        /usr/include \
        /usr/local/include

EXPOSE ${HTTP_SERVER_PORT}