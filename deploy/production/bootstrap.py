"""Initialize a fresh loopback-only deployment; credentials never leave private files."""
import base64
import json
from pathlib import Path
import secrets
import sys
import requests
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding

ROOT = Path('/opt/knowyet-model-gateway')
BASE = 'http://127.0.0.1:3000'

def main():
    session = requests.Session()
    session.trust_env = False
    session.headers['Origin'] = 'https://api.knowyet.com'
    def request(method, path, **kwargs):
        response = session.request(method, BASE+path, timeout=30, **kwargs)
        body = response.json()
        if response.status_code != 200 or body.get('success') is not True:
            raise RuntimeError('Bootstrap API rejected: '+path)
        return body.get('data')
    secret_file = ROOT/'bootstrap-admin.json'
    setup = request('GET', '/api/setup')
    if secret_file.exists():
        credentials = json.loads(secret_file.read_text())
    else:
        if setup.get('status') or setup.get('root_init'):
            raise RuntimeError('Existing administrator requires explicit credentials')
        credentials = dict(username='admin', password=secrets.token_urlsafe(40))
        secret_file.touch(mode=0o600, exist_ok=False)
        secret_file.write_text(json.dumps(credentials))
    if not setup.get('status'):
        request('POST','/api/setup',json=dict(username=credentials['username'], password=credentials['password'], confirmPassword=credentials['password'], SelfUseModeEnabled=False, DemoSiteEnabled=False))
    encryption = request('GET','/api/user/login/encryption-key')
    if not encryption.get('enabled'):
        raise RuntimeError('Expected native login encryption')
    public = serialization.load_pem_public_key(encryption['public_key'].encode())
    encrypted = public.encrypt(credentials['password'].encode(),padding.OAEP(mgf=padding.MGF1(hashes.SHA256()),algorithm=hashes.SHA256(),label=None))
    result = request('POST','/api/user/login',json=dict(username=credentials['username'],password_encrypted=base64.b64encode(encrypted).decode(),encryption_key_id=encryption['kid']))
    session.headers['Authorization'] = 'Bearer '+result['access_token']
    try:
        for key,value in [('ServerAddress','https://api.knowyet.com'),('RegisterEnabled','false'),('PasswordRegisterEnabled','false'),('QuotaForNewUser','0')]:
            request('PUT','/api/option/',json={'key':key,'value':value})
        # Assert that the added API is reachable behind real authentication.
        units = request('GET','/api/billing-units')
        assert isinstance(units,dict) and units['total'] == 0
    finally:
        request('POST','/api/user/auth/logout')
        session.close()
    print('Initialized admin, closed registration, verified billing API; credentials stored privately')

if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('Bootstrap incomplete:', str(error) if isinstance(error, RuntimeError) else type(error).__name__)
        sys.exit(1)
