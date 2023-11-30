#/bin/bash

container_name=dev-hivemind-env

run() {
    tag=$1
    network=$2

    if [ "${tag}" = "" ]; then
        tag=dev-env
    fi

    if [ -n ${network} ]; then
        network_str="--network ${network}"
    fi

    docker run -it --rm \
        --name ${container_name} \
        -v $(pwd):/project \
        --env-file $(pwd)/.env \
        --workdir /project \
        ${network_str} \
        steemit/hivemind:${tag} \
        /bin/bash
}

cli() {
    docker exec -it ${container_name} /bin/bash
}

stop() {
    docker stop ${container_name}
}

main_func() {
    op=$1
    tag=$2
    network=$3

    case ${op} in
        run)
            run $tag $network
            ;;
        cli)
            cli
            ;;
        stop)
            stop
            ;;
        *)
            echo "Unknown Command"
            exit 1
            ;;
    esac
}

main_func $1 $2 $3

