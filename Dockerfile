FROM python:3.5.3
MAINTAINER furion <furion@steemit.com>

COPY . /src
WORKDIR /src

RUN pip install ipython
#RUN pip install -r dev-requirements.txt
RUN pip install -e .

EXPOSE 5000

#CMD ["python", "app.py"]