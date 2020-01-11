#!/bin/bash
#

START_DATE=$1
END_DATE=$2
REPEAT=$3

if [ "$REPEAT" == "" ]; then
    REPEAT="none"
fi

kubectl get service kubecost-turndown-service -n kubecost > /dev/null 2>&1
if [ "$?" == "1" ]; then
    echo "The kubernetes service kubecost-turndown-service does not exist."
    exit 1
fi

service_ip=`kubectl get service kubecost-turndown-service -n kubecost -o jsonpath='{range.status.loadBalancer.ingress[0]}{.ip}{end}'`
if [ "$service_ip" == "" ]; then
    echo "The kubernetes service kubecost-turndown-service has not finished creating..."
    exit 1
fi

target_url=http://$service_ip:9731/schedule

cat <<EOF > data.json
{
    "start":"$START_DATE",
    "end":"$END_DATE",
    "repeat":"$REPEAT"
}
EOF

curl -d "@data.json" -H "Content-Type: application/json" -X POST "$target_url"
echo ""
rm -f data.json